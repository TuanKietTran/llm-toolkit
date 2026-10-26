package server

import (
	"bytes"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"gorouter/internal/config"
)

func TestEmbeddingsFallbackAndModelRewrite(t *testing.T) {
	bad := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(503) }))
	defer bad.Close()
	var gotPath, gotBody string
	good := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		gotPath, gotBody = r.URL.Path, string(b)
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"data":[{"embedding":[0.1]}],"usage":{"prompt_tokens":4}}`)
	}))
	defer good.Close()
	srv := newServer(t,
		[]config.Provider{{ID: "a", Type: "openai", BaseURL: bad.URL}, {ID: "b", Type: "openai", BaseURL: good.URL}},
		[]config.Combo{{Name: "emb", Models: []string{"a/e1", "b/e2"}}})
	defer srv.Close()
	code, body := post(t, srv.URL+"/v1/embeddings", `{"model":"emb","input":"hi"}`)
	if code != 200 || !strings.Contains(body, "embedding") {
		t.Fatalf("got %d %s", code, body)
	}
	if gotPath != "/embeddings" || !strings.Contains(gotBody, `"model":"e2"`) {
		t.Fatalf("path=%q body=%q", gotPath, gotBody)
	}
}

func TestSpeechBinaryPassthrough(t *testing.T) {
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "audio/mpeg")
		w.Write([]byte{0xff, 0xfb, 0x90})
	}))
	defer up.Close()
	srv := newServer(t, []config.Provider{{ID: "a", Type: "openai", BaseURL: up.URL}}, nil)
	defer srv.Close()
	resp, err := http.Post(srv.URL+"/v1/audio/speech", "application/json", strings.NewReader(`{"model":"a/tts","input":"x"}`))
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != 200 || resp.Header.Get("Content-Type") != "audio/mpeg" || len(b) != 3 {
		t.Fatalf("status=%d ct=%s len=%d", resp.StatusCode, resp.Header.Get("Content-Type"), len(b))
	}
}

func TestTranscriptionMultipartModelRewrite(t *testing.T) {
	var gotModel, gotFile string
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r.ParseMultipartForm(1 << 20)
		gotModel = r.FormValue("model")
		f, _, _ := r.FormFile("file")
		b, _ := io.ReadAll(f)
		gotFile = string(b)
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"text":"hello"}`)
	}))
	defer up.Close()
	srv := newServer(t, []config.Provider{{ID: "a", Type: "openai", BaseURL: up.URL}}, []config.Combo{{Name: "stt", Models: []string{"a/whisper-x"}}})
	defer srv.Close()

	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	mw.WriteField("model", "stt")
	fw, _ := mw.CreateFormFile("file", "a.wav")
	fw.Write([]byte("RIFFdata"))
	mw.Close()
	resp, err := http.Post(srv.URL+"/v1/audio/transcriptions", mw.FormDataContentType(), &buf)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != 200 || !strings.Contains(string(body), "hello") || gotModel != "whisper-x" || gotFile != "RIFFdata" {
		t.Fatalf("status=%d body=%s model=%q file=%q", resp.StatusCode, body, gotModel, gotFile)
	}
}
