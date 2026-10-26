package response

import (
	"encoding/json"
	"net/http"
	"time"
)

type R struct {
	Code int         `json:"code"`
	Msg  string      `json:"msg"`
	TS   int64       `json:"ts"`
	Data interface{} `json:"data,omitempty"`
}

func OK(data interface{}) R {
	return R{
		Code: 0,
		Msg:  "操作成功",
		TS:   time.Now().UnixMilli(),
		Data: data,
	}
}

func OKEmpty() R {
	return R{
		Code: 0,
		Msg:  "操作成功",
		TS:   time.Now().UnixMilli(),
	}
}

func Err(code int, msg string) R {
	return R{
		Code: code,
		Msg:  msg,
		TS:   time.Now().UnixMilli(),
	}
}

func ErrDefault(msg string) R {
	return Err(-1, msg)
}

func WriteJSON(w http.ResponseWriter, payload R) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	_ = json.NewEncoder(w).Encode(payload)
}

// WriteJSONStatus 与 WriteJSON 同信封，但允许调用方指定 HTTP 状态码
// （如文件下载端点无快照时的 404；先发头再写体，头须在 WriteHeader 前设好）。
func WriteJSONStatus(w http.ResponseWriter, status int, payload R) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(payload)
}
