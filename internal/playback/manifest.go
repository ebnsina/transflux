package playback

import (
	"bufio"
	"context"
	"fmt"
	"strings"
	"time"
)

// Signer for storage URLs, supplied by the caller so this package does not
// depend on a storage implementation.
type URLSigner func(ctx context.Context, key string, ttl time.Duration) (string, error)

// RewriteHLS turns the relative references in a playlist into URLs a player can
// actually fetch.
//
// A playlist is a list of other things to fetch, and those things are separate
// objects that storage will refuse without a signature of their own. Handing
// out a signed playlist alone therefore delivers nothing.
//
// Playlists point back at this service, because their own contents have to be
// rewritten in turn. Media segments point straight at storage: they are the
// bulk of the bytes, and proxying them would put every viewer's bandwidth
// through the control plane.
func RewriteHLS(ctx context.Context, body, token, prefix string, sign URLSigner,
	ttl time.Duration) (string, error) {

	var out strings.Builder
	scanner := bufio.NewScanner(strings.NewReader(body))
	scanner.Buffer(make([]byte, 0, 64<<10), 4<<20)

	for scanner.Scan() {
		line := scanner.Text()
		trimmed := strings.TrimSpace(line)

		switch {
		case trimmed == "":
			out.WriteString("\n")
			continue

		case strings.HasPrefix(trimmed, "#"):
			// Some tags carry a URI attribute rather than a line of their own,
			// notably the audio group and the initialisation segment.
			rewritten, err := rewriteURIAttribute(ctx, trimmed, token, prefix, sign, ttl)
			if err != nil {
				return "", err
			}
			out.WriteString(rewritten)
			out.WriteString("\n")
			continue

		default:
			// A bare line is something to fetch.
			target, err := resolve(ctx, trimmed, token, prefix, sign, ttl)
			if err != nil {
				return "", err
			}
			out.WriteString(target)
			out.WriteString("\n")
		}
	}
	if err := scanner.Err(); err != nil {
		return "", err
	}
	return out.String(), nil
}

func rewriteURIAttribute(ctx context.Context, line, token, prefix string,
	sign URLSigner, ttl time.Duration) (string, error) {

	const marker = `URI="`
	start := strings.Index(line, marker)
	if start < 0 {
		return line, nil
	}
	rest := line[start+len(marker):]
	end := strings.Index(rest, `"`)
	if end < 0 {
		return line, nil
	}

	target, err := resolve(ctx, rest[:end], token, prefix, sign, ttl)
	if err != nil {
		return "", err
	}
	return line[:start+len(marker)] + target + rest[end:], nil
}

// resolve decides where one reference should point.
func resolve(ctx context.Context, name, token, prefix string, sign URLSigner,
	ttl time.Duration) (string, error) {

	// Already absolute: leave it alone rather than guessing at someone else's
	// intent.
	if strings.HasPrefix(name, "http://") || strings.HasPrefix(name, "https://") {
		return name, nil
	}
	if !SafeFile(name) {
		return "", fmt.Errorf("playlist refers to %q, which is not a file it may reference", name)
	}

	if strings.HasSuffix(name, ".m3u8") {
		return Path(token, name), nil
	}
	return sign(ctx, prefix+"/"+name, ttl)
}

// SafeFile bounds what a manifest, or a request, may name.
//
// Manifests are produced by us, but they are also the input to this rewriting,
// and a path that escapes the package's own prefix must not be reachable
// however it got there.
func SafeFile(name string) bool {
	if name == "" || len(name) > 128 {
		return false
	}
	if strings.ContainsAny(name, "/\\") || strings.Contains(name, "..") {
		return false
	}
	for _, r := range name {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
		case r == '-', r == '_', r == '.':
		default:
			return false
		}
	}
	return true
}
