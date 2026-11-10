package cline

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// NewDirect keeps subscription management independent of traffic proxy settings.
func NewDirect(cookie string) *Client {
	c := New(cookie, "")
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = nil
	c.http.Transport = transport
	c.http.CheckRedirect = func(*http.Request, []*http.Request) error { return fmt.Errorf("订阅管理拒绝重定向") }
	return c
}

// VerifiedIdentity never falls back to cookie text or dashboard HTML.
func (c *Client) VerifiedIdentity(email string) (string, error) {
	raw, err := c.doJSON(http.MethodGet, c.base+"/api/v1/users/me", nil)
	if err != nil {
		return "", fmt.Errorf("授权会话校验失败，请重新正常登录")
	}
	var response struct {
		Success bool `json:"success"`
		Data    struct {
			ID    string `json:"id"`
			Email string `json:"email"`
		} `json:"data"`
	}
	if json.Unmarshal(raw, &response) != nil || !response.Success || !ValidUserID(response.Data.ID) || strings.TrimSpace(email) == "" || !strings.EqualFold(strings.TrimSpace(response.Data.Email), strings.TrimSpace(email)) {
		return "", fmt.Errorf("授权会话与账号邮箱不匹配或用户资料不完整")
	}
	return response.Data.ID, nil
}

type currentPlan struct {
	SubscriptionID string          `json:"subscriptionId"`
	PeriodEnd      json.RawMessage `json:"currentPeriodEnd"`
	CancelAt       json.RawMessage `json:"cancelAt"`
	Plan           json.RawMessage `json:"plan"`
}

func planTimestamp(raw json.RawMessage) int64 {
	var text string
	if json.Unmarshal(raw, &text) != nil {
		text = string(raw)
	}
	if n, err := strconv.ParseInt(text, 10, 64); err == nil {
		if n > 1e12 {
			n /= 1000
		}
		return n
	}
	if parsed, err := time.Parse(time.RFC3339, text); err == nil {
		return parsed.Unix()
	}
	return 0
}

func (c *Client) currentPlan() (currentPlan, error) {
	raw, err := c.doJSON(http.MethodGet, c.base+"/api/v1/users/me/plan", nil)
	if err != nil {
		return currentPlan{}, err
	}
	var response struct {
		Success bool         `json:"success"`
		Data    *currentPlan `json:"data"`
	}
	if json.Unmarshal(raw, &response) != nil || !response.Success || response.Data == nil {
		return currentPlan{}, fmt.Errorf("没有已确认的有效订阅，需人工核对套餐")
	}
	p := *response.Data
	if p.SubscriptionID == "" || len(p.Plan) == 0 || string(p.Plan) == "null" || planTimestamp(p.PeriodEnd) <= time.Now().Unix() {
		return currentPlan{}, fmt.Errorf("当前订阅权益无效或到期时间未知")
	}
	return p, nil
}

// The official Cline web SDK uses DELETE /users/me/plan to schedule cancellation,
// and GET exposes cancelAt/currentPeriodEnd. Never treat the DELETE alone as proof.
func (c *Client) DisableRenewal() error {
	before, err := c.currentPlan()
	if err != nil {
		return err
	}
	if planTimestamp(before.CancelAt) == planTimestamp(before.PeriodEnd) {
		return nil
	}
	if _, err := c.doJSON(http.MethodDelete, c.base+"/api/v1/users/me/plan", nil); err != nil {
		return err
	}
	after, err := c.currentPlan()
	if err != nil {
		return err
	}
	if after.SubscriptionID != before.SubscriptionID || planTimestamp(after.PeriodEnd) != planTimestamp(before.PeriodEnd) || planTimestamp(after.CancelAt) != planTimestamp(after.PeriodEnd) {
		return fmt.Errorf("未确认自动续订已关闭并保留当前权益；已停止后续操作")
	}
	return nil
}
