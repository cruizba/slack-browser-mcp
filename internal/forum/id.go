package forum

import (
	"crypto/rand"
	"strings"
	"sync"
	"time"
)

// ULID-shaped ids: 26 characters of Crockford base32, the first ten the
// millisecond and the rest random, monotonic inside a process. The timestamp prefix
// is why this is not a UUID: ids are typed by people and sorted when two tasks land
// in the same second.
const (
	// IDLength is how long a forum id is.
	IDLength = 26
	// MinPrefix is the shortest prefix that may match: four characters of a ULID
	// carry the clock, so anything shorter would match the whole channel.
	MinPrefix = 4

	alphabet     = "0123456789ABCDEFGHJKMNPQRSTVWXYZ"
	timeChars    = 10
	randomLength = IDLength - timeChars
)

var (
	idMutex    sync.Mutex
	lastTime   int64 = -1
	lastRandom       = ""
)

func encodeBase32(value int64, length int) string {
	out := make([]byte, length)

	for index := length - 1; index >= 0; index-- {
		out[index] = alphabet[value%32]
		value /= 32
	}

	return string(out)
}

func randomChars(length int) string {
	buffer := make([]byte, length)
	_, _ = rand.Read(buffer)

	out := make([]byte, length)

	for index, value := range buffer {
		out[index] = alphabet[int(value)%32]
	}

	return string(out)
}

// increment is a base32 increment from the right, so an id from the same
// millisecond still sorts later than the one before it.
func increment(value string) string {
	chars := []byte(value)

	for index := len(chars) - 1; index >= 0; index-- {
		at := strings.IndexByte(alphabet, chars[index])
		if at < len(alphabet)-1 {
			chars[index] = alphabet[at+1]

			return string(chars)
		}

		chars[index] = alphabet[0]
	}

	return randomChars(len(value))
}

// NewID makes one id, ordered against every id this process made before it.
func NewID(now time.Time) string {
	idMutex.Lock()
	defer idMutex.Unlock()

	milliseconds := now.UnixMilli()

	if milliseconds > lastTime {
		lastTime = milliseconds
		lastRandom = randomChars(randomLength)
	} else {
		// Same millisecond, or a clock that stepped backwards: keep the last stamp so
		// ids stay ordered even when the clock does not.
		lastRandom = increment(lastRandom)
	}

	return encodeBase32(lastTime, timeChars) + lastRandom
}

// NormalizeID uppercases an id and folds the letters Crockford leaves out, because
// a person copying one out of Slack will get them wrong eventually.
func NormalizeID(raw string) string {
	return strings.NewReplacer("I", "1", "L", "1", "O", "0").Replace(strings.ToUpper(strings.TrimSpace(raw)))
}

// IsID reports whether a string is a whole forum id.
func IsID(value string) bool {
	if len(value) != IDLength {
		return false
	}

	for _, letter := range value {
		if !strings.ContainsRune(alphabet, letter) {
			return false
		}
	}

	return true
}

// MatchesID reports whether an id starts with a prefix long enough to be safe.
func MatchesID(id, prefix string) bool {
	wanted := NormalizeID(prefix)

	return len(wanted) >= MinPrefix && strings.HasPrefix(NormalizeID(id), wanted)
}
