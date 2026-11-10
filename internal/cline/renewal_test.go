package cline

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"
)

func TestDisableRenewalRechecksCurrentEntitlement(t *testing.T) {
	end := time.Now().Add(24 * time.Hour).Unix()
	var calls []string
	cancelled := false
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls = append(calls, r.Method)
		if r.Method == "DELETE" {
			cancelled = true
			w.WriteHeader(204)
			return
		}
		cancel := "null"
		if cancelled {
			cancel = fmt.Sprint(end)
		}
		fmt.Fprintf(w, `{"success":true,"data":{"subscriptionId":"sub-test","currentPeriodEnd":%d,"cancelAt":%s,"plan":{"id":"test"}}}`, end, cancel)
	}))
	defer s.Close()
	c := New("synthetic-cookie", "")
	c.SetBase(s.URL, s.URL)
	if err := c.DisableRenewal(); err != nil {
		t.Fatal(err)
	}
	if fmt.Sprint(calls) != "[GET DELETE GET]" {
		t.Fatalf("must read, cancel and verify: %v", calls)
	}
	if err := c.DisableRenewal(); err != nil {
		t.Fatal(err)
	}
	if fmt.Sprint(calls) != "[GET DELETE GET GET]" {
		t.Fatal("already-cancelled renewal must not be cancelled again")
	}
}

func TestDisableRenewalRejectsUnconfirmedOrLostPlan(t *testing.T) {
	for _, mode := range []string{"no-plan", "unchanged", "lost-entitlement"} {
		t.Run(mode, func(t *testing.T) {
			cancelled := false
			s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method == "DELETE" {
					cancelled = true
					w.WriteHeader(204)
					return
				}
				if mode == "no-plan" || mode == "lost-entitlement" && cancelled {
					fmt.Fprint(w, `{"success":true,"data":null}`)
					return
				}
				fmt.Fprintf(w, `{"success":true,"data":{"subscriptionId":"sub-test","currentPeriodEnd":%d,"cancelAt":null,"plan":{"id":"test"}}}`, time.Now().Add(time.Hour).Unix())
			}))
			defer s.Close()
			c := New("synthetic-cookie", "")
			c.SetBase(s.URL, s.URL)
			if err := c.DisableRenewal(); err == nil {
				t.Fatal("unknown cancellation or lost entitlement must fail closed")
			}
		})
	}
}

func TestDirectRejectsRedirectAndDoesNotLeakCookie(t *testing.T) {
	leaked := false
	destination := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { leaked = true }))
	defer destination.Close()
	source := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, destination.URL, http.StatusTemporaryRedirect)
	}))
	defer source.Close()
	c := NewDirect("session=synthetic")
	c.SetBase(source.URL, source.URL)
	if err := c.DisableRenewal(); err == nil || leaked {
		t.Fatal("redirect must fail before forwarding credentials")
	}
}

func TestVerifiedIdentityRequiresMatchingOfficialProfile(t *testing.T) {
	for _, body := range []string{`{"success":true,"data":{"id":"usr-test","email":"Owner@example.com"}}`, `{"success":true,"data":{"id":"usr-test","email":"other@example.com"}}`, `{"success":false,"data":{"id":"usr-test","email":"Owner@example.com"}}`} {
		s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, body) }))
		c := NewDirect("session=test")
		c.SetBase(s.URL, s.URL)
		id, err := c.VerifiedIdentity("owner@example.com")
		if body == `{"success":true,"data":{"id":"usr-test","email":"Owner@example.com"}}` {
			if err != nil || id != "usr-test" {
				t.Fatal("valid profile rejected")
			}
		} else if err == nil {
			t.Fatal("unverified identity accepted")
		}
		s.Close()
	}
}

func TestDirectNeverUsesInheritedProxy(t *testing.T) {
	proxyUsed := false
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.IsAbs() {
			proxyUsed = true
		}
		fmt.Fprint(w, `{"success":true,"data":{"id":"usr-test","email":"owner@example.com"}}`)
	}))
	defer s.Close()
	original := http.DefaultTransport
	transport := original.(*http.Transport).Clone()
	transport.Proxy = func(*http.Request) (*url.URL, error) { return url.Parse("http://proxy.invalid") }
	address := s.Listener.Addr().String()
	transport.DialContext = func(ctx context.Context, network, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, network, address)
	}
	http.DefaultTransport = transport
	defer func() { http.DefaultTransport = original; transport.CloseIdleConnections() }()
	c := NewDirect("session=synthetic")
	c.SetBase("http://official.invalid", s.URL)
	if _, err := c.VerifiedIdentity("owner@example.com"); err != nil || proxyUsed {
		t.Fatal("direct subscription client inherited proxy")
	}
}

func TestDisableRenewalRejectsCancellationAfterNextBillingDate(t *testing.T) {
	end := time.Now().Add(time.Hour).Unix()
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, `{"success":true,"data":{"subscriptionId":"sub-test","currentPeriodEnd":%d,"cancelAt":%d,"plan":{}}}`, end, end+3600)
	}))
	defer s.Close()
	c := NewDirect("session=synthetic")
	c.SetBase(s.URL, s.URL)
	if err := c.DisableRenewal(); err == nil {
		t.Fatal("cancellation after next billing does not prove renewal is off")
	}
}
