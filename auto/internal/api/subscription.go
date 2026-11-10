package api

import (
	"encoding/json"
	"io"
	"net/http"
)

func (s *Server) subscriptionBatch(w http.ResponseWriter, r *http.Request) {
	if !emptyObject(w, r) {
		return
	}
	id := r.PathValue("id")
	if _, err := s.store.GetBatch(id); err != nil {
		writeErr(w, http.StatusNotFound, "批次不存在")
		return
	}
	jobs, err := s.jobs.EnqueueBatchSubscription(id)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusAccepted, jobs)
}

// emptyObject 只接受空对象，避免把登录或支付选项混进后处理接口。
func emptyObject(w http.ResponseWriter, r *http.Request) bool {
	var options map[string]json.RawMessage
	decoder := json.NewDecoder(r.Body)
	if decoder.Decode(&options) != nil || options == nil || len(options) != 0 {
		writeErr(w, http.StatusBadRequest, "订阅后处理只接受空对象，不接受登录、注册或支付选项")
		return false
	}
	if decoder.Decode(new(any)) != io.EOF {
		writeErr(w, http.StatusBadRequest, "只能提交一个 JSON 对象")
		return false
	}
	return true
}

func (s *Server) subscriptionAccount(w http.ResponseWriter, r *http.Request) {
	if !emptyObject(w, r) {
		return
	}
	id := r.PathValue("id")
	if _, err := s.store.Get(id); err != nil {
		writeErr(w, http.StatusNotFound, "账号不存在或无法读取")
		return
	}
	j, err := s.jobs.EnqueueSubscription(id)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusAccepted, j)
}
