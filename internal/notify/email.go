package notify

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"

	"golang.org/x/oauth2"
	"golang.org/x/oauth2/clientcredentials"
)

// EmailConfig sends mail through Microsoft Graph from a shared mailbox, with
// the client credentials of an Azure app registration.
type EmailConfig struct {
	TenantID     string `json:"tenant_id"`
	ClientID     string `json:"client_id"`
	ClientSecret string `json:"client_secret"` //nolint:gosec // G117: secret belongs in this config
	FromAddress  string `json:"from_address"`
	Recipients   string `json:"recipients"` // comma-separated
	Events       Events `json:"events"`
}

const graphURL = "https://graph.microsoft.com/v1.0"

func (c *EmailConfig) configured() bool {
	return c.TenantID != "" && c.ClientID != "" && c.ClientSecret != "" && c.FromAddress != "" && c.Recipients != ""
}

func (c *EmailConfig) send(ctx context.Context, e *Event) error {
	type address struct {
		Address string `json:"address"`
	}
	type recipient struct {
		EmailAddress address `json:"emailAddress"`
	}
	var to []recipient
	for a := range strings.SplitSeq(c.Recipients, ",") {
		if a = strings.TrimSpace(a); a != "" {
			to = append(to, recipient{address{a}})
		}
	}
	body := fmt.Sprintf("%s\n\nTime: %s\n", e.Message(), e.Time.Format("2006-01-02 15:04:05 MST"))
	msg, err := json.Marshal(map[string]any{"message": map[string]any{
		"subject":      e.Message(),
		"body":         map[string]string{"contentType": "Text", "content": body},
		"toRecipients": to,
	}})
	if err != nil {
		return err
	}

	creds := clientcredentials.Config{
		ClientID:     c.ClientID,
		ClientSecret: c.ClientSecret,
		TokenURL:     "https://login.microsoftonline.com/" + url.PathEscape(c.TenantID) + "/oauth2/v2.0/token",
		Scopes:       []string{"https://graph.microsoft.com/.default"},
	}
	client := creds.Client(context.WithValue(ctx, oauth2.HTTPClient, httpClient))
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		graphURL+"/users/"+url.PathEscape(c.FromAddress)+"/sendMail", bytes.NewReader(msg))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("email: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusAccepted {
		detail, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return fmt.Errorf("graph API returned %d: %s", resp.StatusCode, detail)
	}
	return nil
}
