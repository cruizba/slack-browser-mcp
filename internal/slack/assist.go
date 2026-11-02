package slack

import (
	"github.com/cruizba/slack-browser-mcp/internal/config"
	"github.com/cruizba/slack-browser-mcp/internal/failure"
)

// The mark a message carries when an agent wrote it on the user's behalf: a rule
// above and below, one bold line naming the bot, and the text in between, so the
// block reads as one delivered message rather than something typed into the channel.
//
// Slack renders the rules as divider blocks, not as characters: a plain text line
// arrives glued to the author's name, which Slack prints immediately above the
// message, and neither a leading newline nor a zero-width space survives that trim.
const (
	// AssistedHeader is the line that says an agent wrote it.
	AssistedHeader = "Message Sent by AI"
	// AssistedRule is the plain-text stand-in for the divider blocks.
	AssistedRule = "----------------------------------------"
	// MaxTextChars is where Slack refuses a message instead of truncating it.
	MaxTextChars = 4000
	// maxSectionChars is where Slack cuts a section block, silently.
	maxSectionChars = 2900
)

// Block is one Slack block. Only the two this tool posts are modelled.
type Block struct {
	Type string     `json:"type"`
	Text *BlockText `json:"text,omitempty"`
}

// BlockText is the mrkdwn body of a section.
type BlockText struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

// Signed is what a send carries: the fallback text and, when it fits, the blocks
// that render it.
type Signed struct {
	Text   string
	Blocks []Block
}

// DescribeSigning is the one line both surfaces print about signing.
func DescribeSigning(signing config.Signing, agent string) string {
	switch signing.Kind {
	case config.SigningNickname:
		return "assisted nickname: " + signing.Nickname
	case config.SigningOff:
		return "assisted nickname off: posted messages carry no header"
	case config.SigningDefault:
		return "no nickname configured: each send is signed as *" + AssistedHeader + " " +
			config.DefaultNickname(agent, "") + " of <the sending account>*"
	default:
		return "assisted nickname: " + signing.Nickname
	}
}

// SignedMessage wraps the text in the AI-assisted block. It refuses what Slack
// would cut: a message with its tail missing is a wrong message, and the caller can
// shorten it.
func SignedMessage(text, nickname string) (Signed, error) {
	header := "*" + AssistedHeader + " " + nickname + "*"
	fallback := AssistedRule + "\n\n" + header + "\n\n" + text + "\n\n" + AssistedRule

	if len([]rune(fallback)) > MaxTextChars {
		return Signed{}, &failure.StoreError{Message: "The message plus its AI-assisted header is " +
			itoa(len([]rune(fallback))) + " characters; Slack accepts " + itoa(MaxTextChars) +
			" and would cut the end off, so shorten the text."}
	}

	if len([]rune(text)) > maxSectionChars {
		return Signed{Text: fallback}, nil
	}

	return Signed{
		Text: fallback,
		Blocks: []Block{
			{Type: "divider"},
			{Type: "section", Text: &BlockText{Type: "mrkdwn", Text: header}},
			{Type: "section", Text: &BlockText{Type: "mrkdwn", Text: text}},
			{Type: "divider"},
		},
	}, nil
}
