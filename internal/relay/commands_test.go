package relay

import (
	"context"
	"strings"
	"testing"
)

func TestStartCarriesPrivacyNotice(t *testing.T) {
	h := newHarness(t)
	h.srv.opts.PublicURL = "https://alerts.example.org"
	if err := h.srv.HandleUpdate(context.Background(), Update{ChatID: 7, Text: "/start"}); err != nil {
		t.Fatal(err)
	}
	msgs := h.sender.forChat(7)
	last := msgs[len(msgs)-1]
	if !strings.Contains(last, "Privacy notice") || !strings.Contains(last, "alerts\\.example\\.org") || !strings.Contains(last, "public IP address") {
		t.Errorf("/start without the notice: %q", last)
	}
	if !strings.Contains(last, "pairing code") {
		t.Errorf("/start lost the code: %q", last)
	}
}
