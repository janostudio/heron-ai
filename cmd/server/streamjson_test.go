package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestStreamJSONMessageContentSupportsMultimediaBlocks(t *testing.T) {
	raw := json.RawMessage(`{"role":"user","content":[
		{"type":"text","text":"请分析图片"},
		{"type":"image","source":{"type":"base64","media_type":"image/png","data":"cG5n"}}
	]}`)
	text, parts, err := parseStreamJSONContent(raw)
	require.NoError(t, err)
	require.Equal(t, "请分析图片", text)
	require.Len(t, parts, 1)
	require.Equal(t, "image", parts[0].Type)
	require.Equal(t, "base64", parts[0].Media.SourceType)
}

func TestURLQueryEscape(t *testing.T) {
	for _, test := range []struct {
		name  string
		input string
	}{
		{name: "plain", input: "fs-123"},
		{name: "spaces", input: "fs 123 abc"},
		{name: "question mark", input: "fs?123"},
		{name: "ampersand", input: "fs&123"},
		{name: "equals", input: "fs=123"},
		{name: "hash", input: "fs#123"},
		{name: "plus", input: "fs+123"},
		{name: "semicolon", input: "fs;123"},
		{name: "slash", input: "fs/123"},
		{name: "unicode chinese", input: "会话-id-中文"},
		{name: "mixed", input: "a b?c&d=e#f+g;h/i"},
		{name: "empty", input: ""},
	} {
		t.Run(test.name, func(t *testing.T) {
			got := urlQueryEscape(test.input)
			want := url.QueryEscape(test.input)
			require.Equal(t, want, got)
		})
	}
}

func TestStreamJSONClientDo(t *testing.T) {
	t.Run("200 ok", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			w.Write([]byte(`{"ok":true}`))
		}))
		defer server.Close()

		client := &streamJSONClient{baseURL: server.URL}
		req, err := http.NewRequest(http.MethodGet, server.URL+"/api/result?session_id=x", nil)
		require.NoError(t, err)

		raw, err := client.do(req)
		require.NoError(t, err)
		require.JSONEq(t, `{"ok":true}`, string(raw))
	})

	t.Run("non-2xx with body", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusInternalServerError)
			w.Write([]byte("boom"))
		}))
		defer server.Close()

		client := &streamJSONClient{baseURL: server.URL}
		req, err := http.NewRequest(http.MethodGet, server.URL+"/api/result", nil)
		require.NoError(t, err)

		raw, err := client.do(req)
		require.Error(t, err)
		require.Nil(t, raw)
		require.Contains(t, err.Error(), "500")
		require.Contains(t, err.Error(), "boom")
	})

	t.Run("connection error", func(t *testing.T) {
		client := &streamJSONClient{baseURL: "http://127.0.0.1:1"}
		req, err := http.NewRequest(http.MethodGet, "http://127.0.0.1:1/api/result", nil)
		require.NoError(t, err)

		raw, err := client.do(req)
		require.Error(t, err)
		require.Nil(t, raw)
	})

	t.Run("read error", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Length", "100")
			w.Write([]byte("short"))
		}))
		defer server.Close()

		client := &streamJSONClient{baseURL: server.URL}
		req, err := http.NewRequest(http.MethodGet, server.URL+"/api/result", nil)
		require.NoError(t, err)

		raw, err := client.do(req)
		require.Error(t, err)
		require.Nil(t, raw)
	})
}

func TestStreamJSONClientPost(t *testing.T) {
	var gotBody []byte
	var gotPath string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.String()
		gotBody = make([]byte, r.ContentLength)
		r.Body.Read(gotBody)
		w.Write([]byte(`{"ok":true}`))
	}))
	defer server.Close()

	client := &streamJSONClient{baseURL: server.URL}
	raw, err := client.post("/api/approvals?session_id=fs%2F1", map[string]any{"approved": true})
	require.NoError(t, err)
	require.JSONEq(t, `{"ok":true}`, string(raw))
	require.Equal(t, "/api/approvals?session_id=fs%2F1", gotPath)
	require.JSONEq(t, `{"approved":true}`, string(gotBody))
}

func TestStreamJSONClientGet(t *testing.T) {
	var gotPath string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.String()
		w.Write([]byte(`{"ok":true}`))
	}))
	defer server.Close()

	client := &streamJSONClient{baseURL: server.URL}
	raw, err := client.get("/api/result?session_id=fs%2F1")
	require.NoError(t, err)
	require.JSONEq(t, `{"ok":true}`, string(raw))
	require.Equal(t, "/api/result?session_id=fs%2F1", gotPath)
}

func TestStreamJSONClientServeApprovalUsesEscapedSessionID(t *testing.T) {
	var gotPath string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.RequestURI()
		w.Write([]byte(`{"session":{"id":"fs/1","status":"waiting_input"},"reply":"ok"}`))
	}))
	defer server.Close()

	client := &streamJSONClient{
		baseURL: server.URL,
		out:     bufio.NewWriter(&bytes.Buffer{}),
	}
	// Use a session_id containing characters that the old replacer mishandled.
	input := `{"type":"approval","session_id":"fs/1+半","approval_id":"a1","approved":true}` + "\n"
	err := client.Serve(strings.NewReader(input))
	require.NoError(t, err)
	require.Contains(t, gotPath, url.QueryEscape("fs/1+半"))
}
