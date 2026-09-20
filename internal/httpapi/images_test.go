package httpapi

import (
	"bytes"
	"context"
	"image"
	"image/png"
	"io"
	"net/http"
	"testing"
)

func TestImageDiskCacheAndDeletion(t *testing.T) {
	env := newTestEnv(t)
	response, body := doJSON(t, http.MethodPost, env.server.URL+"/api/v1/snapshots", "", map[string]any{"url": "https://chatgpt.com/share/image-test"})
	if response.StatusCode != 201 {
		t.Fatal("import failed")
	}
	id := body["id"].(string)
	var data bytes.Buffer
	_ = png.Encode(&data, image.NewRGBA(image.Rect(0, 0, 2, 2)))
	if err := env.files.WriteFileAtomic("conversations/"+id+"/images/file_cached", data.Bytes()); err != nil {
		t.Fatal(err)
	}
	target := env.server.URL + "/media/" + id + "/file_cached"
	resp, err := http.Get(target)
	if err != nil {
		t.Fatal(err)
	}
	got, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != 200 || !bytes.Equal(got, data.Bytes()) || resp.Header.Get("Content-Type") != "image/png" {
		t.Fatal("cache did not serve image")
	}
	req, _ := http.NewRequest("GET", target, nil)
	req.Header.Set("If-None-Match", resp.Header.Get("ETag"))
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 304 {
		t.Fatal("conditional cache missed")
	}
	resp, err = http.Get(env.server.URL + "/media/" + id + "/file_not_in_conversation")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 404 {
		t.Fatal("unrelated image allowed")
	}
	if err := env.store.MarkDeleted(context.Background(), id); err != nil {
		t.Fatal(err)
	}
	resp, err = http.Get(target)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 404 {
		t.Fatal("deleted snapshot exposed cached image")
	}
}
