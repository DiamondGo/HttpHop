package server

import (
	"net/http"
	"net/url"
	"path"
	"strings"

	"github.com/DiamondGo/HttpHop/internal/router"
)

// rewritePublicPrefixHeaders adjusts response headers so redirects and cookies
// work when strip_prefix removed a public path segment on the request path.
// Equivalent to nginx proxy_redirect and proxy_cookie_path for the common case.
func rewritePublicPrefixHeaders(resp *http.Response, publicPrefix string) {
	publicPrefix = router.NormalizePathPrefix(publicPrefix)
	if publicPrefix == "" {
		return
	}

	if loc := resp.Header.Get("Location"); loc != "" {
		if rewritten := rewriteLocation(loc, publicPrefix); rewritten != loc {
			resp.Header.Set("Location", rewritten)
		}
	}

	cookies := resp.Header.Values("Set-Cookie")
	if len(cookies) == 0 {
		return
	}
	resp.Header.Del("Set-Cookie")
	for _, c := range cookies {
		resp.Header.Add("Set-Cookie", rewriteSetCookiePath(c, publicPrefix))
	}
}

func rewriteLocation(loc, publicPrefix string) string {
	if strings.HasPrefix(loc, "/") {
		return prependPublicPath(publicPrefix, loc)
	}

	u, err := url.Parse(loc)
	if err != nil || u.Path == "" || !strings.HasPrefix(u.Path, "/") {
		return loc
	}

	newPath := prependPublicPath(publicPrefix, u.Path)
	if newPath == u.Path {
		return loc
	}
	u.Path = newPath
	return u.String()
}

func prependPublicPath(publicPrefix, p string) string {
	publicPrefix = router.NormalizePathPrefix(publicPrefix)
	if publicPrefix == "" {
		return p
	}

	cleaned := path.Clean(p)
	if cleaned == "/" {
		return publicPrefix
	}
	if cleaned == publicPrefix || strings.HasPrefix(cleaned, publicPrefix+"/") {
		return cleaned
	}
	return publicPrefix + cleaned
}

func rewriteSetCookiePath(setCookie, publicPrefix string) string {
	parts := strings.Split(setCookie, ";")
	if len(parts) == 0 {
		return setCookie
	}

	changed := false
	for i := 1; i < len(parts); i++ {
		trimmed := strings.TrimSpace(parts[i])
		if len(trimmed) < 5 || !strings.EqualFold(trimmed[:5], "path=") {
			continue
		}
		rawPath := strings.TrimSpace(trimmed[5:])
		newPath := rewriteCookiePath(rawPath, publicPrefix)
		if newPath != rawPath {
			parts[i] = " Path=" + newPath
			changed = true
		}
		break
	}

	if !changed {
		return setCookie
	}

	return strings.Join(parts, ";")
}

func rewriteCookiePath(cookiePath, publicPrefix string) string {
	publicPrefix = router.NormalizePathPrefix(publicPrefix)
	if publicPrefix == "" {
		return cookiePath
	}

	p := cookiePath
	if p == "" {
		p = "/"
	}
	if !strings.HasPrefix(p, "/") {
		p = "/" + p
	}
	return prependPublicPath(publicPrefix, p)
}
