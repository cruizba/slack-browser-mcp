package slack_test

import (
	"strings"
	"testing"

	"github.com/cruizba/slack-browser-mcp/internal/config"
	"github.com/cruizba/slack-browser-mcp/internal/slack"
)

// The signed block is what a reader sees instead of the bare message, and it is the
// proof that a machine wrote it. Two things matter: the fallback text is the message
// even in a client that does not render blocks, and Slack's two length limits decide
// whether the blocks travel at all — a message with its tail cut off is a wrong
// message, so over the limit it is refused rather than shortened.

func TestSignedMessageWrapsInRulesAndBlocks(t *testing.T) {
	t.Parallel()

	signed, err := slack.SignedMessage("the tests pass", "ana-cli")
	if err != nil {
		t.Fatalf("SignedMessage: %v", err)
	}

	wantText := slack.AssistedRule + "\n\n*" + slack.AssistedHeader + " ana-cli*\n\nthe tests pass\n\n" + slack.AssistedRule
	if signed.Text != wantText {
		t.Fatalf("text = %q", signed.Text)
	}

	// Four blocks: rule, header, text, rule. A client that renders blocks must show the
	// same thing the fallback says.
	if len(signed.Blocks) != 4 {
		t.Fatalf("blocks = %+v", signed.Blocks)
	}

	if signed.Blocks[0].Type != "divider" || signed.Blocks[3].Type != "divider" {
		t.Fatalf("the rules are not dividers: %+v", signed.Blocks)
	}

	if signed.Blocks[1].Text.Text != "*"+slack.AssistedHeader+" ana-cli*" {
		t.Fatalf("header block = %+v", signed.Blocks[1].Text)
	}

	if signed.Blocks[2].Text.Text != "the tests pass" {
		t.Fatalf("body block = %+v", signed.Blocks[2].Text)
	}

	for _, block := range signed.Blocks {
		if block.Text != nil && block.Text.Type != "mrkdwn" {
			t.Fatalf("a block is not mrkdwn: %+v", block.Text)
		}
	}
}

func TestSignedMessageRefusesWhatSlackWouldCut(t *testing.T) {
	t.Parallel()

	// Exactly at the limit is fine; one over is refused, because Slack truncates and a
	// truncated message is worse than a refusal. The room left for the body is the limit
	// minus the two rules, the header and the four blank lines between them.
	nickname := "ana"
	header := "*" + slack.AssistedHeader + " " + nickname + "*"
	overhead := len([]rune(slack.AssistedRule))*2 + len([]rune(header)) + 6
	room := slack.MaxTextChars - overhead

	if _, err := slack.SignedMessage(strings.Repeat("a", room), nickname); err != nil {
		t.Fatalf("a message that just fits was refused: %v", err)
	}

	_, err := slack.SignedMessage(strings.Repeat("a", room+1), nickname)
	if err == nil {
		t.Fatal("a message one rune over the limit was accepted")
	}

	// The error has to say the size and the limit, or the caller cannot know how much
	// to cut.
	if !strings.Contains(err.Error(), "4000") || !strings.Contains(err.Error(), "shorten") {
		t.Fatalf("error = %q", err)
	}
}

func TestSignedMessageDropsBlocksForALongBody(t *testing.T) {
	t.Parallel()

	// Slack limits a single section block far below the message limit, so a long body
	// travels as text only. The point of the test is that the text is never dropped:
	// the message still arrives whole, without the blocks.
	body := strings.Repeat("a", 2901)

	signed, err := slack.SignedMessage(body, "ana")
	if err != nil {
		t.Fatalf("SignedMessage: %v", err)
	}

	if len(signed.Blocks) != 0 {
		t.Fatalf("blocks = %d, want none over the section limit", len(signed.Blocks))
	}

	if !strings.Contains(signed.Text, body) {
		t.Fatal("the body was lost when the blocks were dropped")
	}
}

func TestDescribeSigning(t *testing.T) {
	t.Parallel()

	nickname := slack.DescribeSigning(config.Signing{Kind: config.SigningNickname, Nickname: "ana-cli"}, "cursor")
	if !strings.Contains(nickname, "ana-cli") {
		t.Fatalf("nickname = %q", nickname)
	}

	off := slack.DescribeSigning(config.Signing{Kind: config.SigningOff}, "cursor")
	if !strings.Contains(off, "off") || !strings.Contains(off, "no header") {
		t.Fatalf("off = %q", off)
	}

	// The generated default names the client, because the reader needs to know which
	// tool wrote it even when nobody configured a nickname.
	def := slack.DescribeSigning(config.Signing{Kind: config.SigningDefault}, "cursor")
	if !strings.Contains(def, "cursor") || !strings.Contains(def, slack.AssistedHeader) {
		t.Fatalf("default = %q", def)
	}
}
