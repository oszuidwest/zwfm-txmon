package notify

import (
	"context"
	"strings"
	"testing"
)

func TestWebhookErrorHidesURL(t *testing.T) {
	for _, u := range []string{"http://127.0.0.1:1/hook?token=s3cret", "http://[::1/hook?token=s3cret"} {
		c := WebhookConfig{URL: u}
		err := c.send(context.Background(), &Event{})
		if err == nil || strings.Contains(err.Error(), "s3cret") {
			t.Errorf("%s: error %v, want one without the URL", u, err)
		}
	}
}
