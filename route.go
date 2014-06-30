// Package route implements an http.Handler for request routing.  It
// is based off of github.com/bmizerany/pat and
// github.com/gorilla/mux, with my personal flavor of features:
//
// HTTP verbs/methods are specified in the routes.
//
// No redirects. If a pattern could be matched by concatenating or
// truncating a trailing slash, the corresponding HandlerFunc is
// called directly rather than redirecting the client.
//
//   route.Get("/foo", GetFoo)  // matches "/foo" and "/foo/"
//   route.Get("/foo/", GetFoo) // panics because it is effectively the same pattern
//
// Patterns are not prefixes, they match the entire path. Regular
// expressions aren't allowed but variables are. A variable can match
// either a single path element, or a path suffix.
//
//   route.Get("/users/:userID/posts/:postID", GetPost)  // matches "/users/1234/posts/123"
//   route.Get("/static/*filepath", GetStatic)           // matches "/static/js/jquery.js"
//   route.Get("/static/*filepath/foo", GetStaticFoo)    // panics
//
// Captured variables are appended to the request URL's query making
// them accessible via the request's FormValue method.
//
//   id := req.FormValue(":userID")
//   fp := req.FormValue("*filepath")
//
// You can get back the original query string if you need it:
//
//   q := route.StripVars(req.URL.RawQuery)
//
// Get, Put, and the others, panic if the pattern conflicts with
// another one.
//
//   route.Get("/", GetRoot)
//   route.Get("/*path", GetAnythingButRoot)
//   route.Get("/foo", GetFoo)            // panics
//
// Routes can optionally be named, that way you can construct a url
// that would match the route.
//
//   route.Get("/users/:userID/posts/:postID", GetPost, "post")
//   ...
//   http.Redirect(w, r, route.URL("post", userID, postID), 303)
//
// There are hooks for 404 and 405 errors that would normally be
// handled by the router, that way you can serve what ever you
// want. The "Allow" header is added on 405 errors before calling your
// handler.
//
//   route.Handle404(func(w http.ResponseWriter, r *http.Request) {
//     w.Header().Set("Content-Type", "application/json")
//     w.WriteHeader(404)
//     w.Write([]byte(`{"error":404}`))
//   })
//
// There is also a hook for panics that bubble up to the router from
// your handlers. It takes one argument, the thing that was passed to
// the panic itself. Here's how you might log a panic:
//
//   route.HandlePanic(func(r *http.Request, e interface{}) {
//     const size = 64 << 10
//     buf := make([]byte, size)
//     buf = buf[:runtime.Stack(buf, false)]
//     log.Printf("panic at %s: %v\n%s", r.URL, e, buf)
//   })
//
// The pattern registration methods are all 3 letters so that the
// patterns are aligned. Also, patterns can be specified in any order
// you want and you'll get the same behavior.
//
//   route.Get("/", GetRoot)
//   route.Get("/static/*filepath", GetStatic)
//   route.Get("/signin", GetSignin)
//   route.Pst("/signin", PostSignin)
//   route.Get("/signout", GetSignout)
//   route.Get("/accounts/:accountID", GetAccount)
//   route.Put("/accounts/:accountID", PutAccount)
//   route.Del("/accounts/:accountID", DelAccount)
//   route.Pst("/accounts/:accountID/posts", PostPost)
//   route.Get("/accounts/:accountID/posts/:postID", GetPost)
//
//   log.Fatal(http.ListenAndServe(":8080", route.DefaultHandler))
//
// Lastly, there is no locking. You should register HandlerFuncs from
// a single thread.
//
package route

import (
	"net/http"
	"net/url"
	"path"
	"strings"
)

var DefaultHandler = &Handler{}

// Match registers a pattern with the given method on the
// DefaultHandler with an optional name.
func Match(method, pat string, f http.HandlerFunc, name ...string) {
	DefaultHandler.Match(method, pat, f, name...)
}

// Get registers a pattern with method "GET" on the DefaultHandler.
func Get(pat string, f http.HandlerFunc, name ...string) {
	DefaultHandler.Get(pat, f, name...)
}

// Pst registers a pattern with method "POST" on the DefaultHandler.
func Pst(pat string, f http.HandlerFunc, name ...string) {
	DefaultHandler.Pst(pat, f, name...)
}

// Put registers a pattern with method "PUT" on the DefaultHandler.
func Put(pat string, f http.HandlerFunc, name ...string) {
	DefaultHandler.Put(pat, f, name...)
}

// Del registers a pattern with method "DELETE" on the DefaultHandler.
func Del(pat string, f http.HandlerFunc, name ...string) {
	DefaultHandler.Del(pat, f, name...)
}

// Opt registers a pattern with method "OPTIONS" on the
// DefaultHandler.
func Opt(pat string, f http.HandlerFunc, name ...string) {
	DefaultHandler.Opt(pat, f, name...)
}

func Handle404(f http.HandlerFunc) {
	DefaultHandler.Handle404 = f
}

func Handle405(f http.HandlerFunc) {
	DefaultHandler.Handle405 = f
}

func HandlePanic(f func(*http.Request, interface{})) {
	DefaultHandler.HandlePanic = f
}

// URL constructs a url that would match the named pattern. Variables
// must be provided in the same order as they appear in the pattern.
func URL(name string, args ...string) string {
	return DefaultHandler.URL(name, args...)
}

// StripVars removes any variables that were added to the query by the
// Handler.
func StripVars(q string) string {
	colon := url.QueryEscape(":")
	star := url.QueryEscape("*")
	for {
		i := strings.LastIndex(q, "&")
		if i == -1 {
			if len(q) >= len(colon) && q[:len(colon)] == colon {
				return ""
			}
			if len(q) >= len(star) && q[:len(star)] == star {
				return ""
			}
			return q
		}
		if len(q[i+1:]) >= len(colon) && q[i+1:i+1+len(colon)] == colon {
			q = q[:i]
			continue
		}
		if len(q[i+1:]) >= len(star) && q[i+1:i+1+len(star)] == star {
			q = q[:i]
			continue
		}
		return q
	}
}

type Handler struct {
	Handle404   http.HandlerFunc
	Handle405   http.HandlerFunc
	HandlePanic func(*http.Request, interface{}) // Takes the value that was passed to the panic.

	trie trie
	pats map[string]string
}

type trie struct {
	t       map[string]*trie
	verbs   map[string]http.HandlerFunc
	varName string
}

func (h *Handler) Match(method, pat string, f http.HandlerFunc, name ...string) {
	if pat == "" {
		panic(`route: "" is not a valid pattern"`)
	}
	if f == nil {
		panic("route: nil is not a valid HandlerFunc")
	}
	if len(name) > 1 {
		panic("route: a pattern can have only one name")
	}
	if len(name) == 1 {
		if _, ok := h.pats[name[0]]; ok {
			panic("route: there is a registered pattern by the same name")
		}
	}
	p := path.Clean(pat)
	parts := []string{}
	if p != "/" {
		parts = strings.Split(p[1:], "/")
	}
	t := &h.trie
	for i, part := range parts {
		// Is part a :var?
		if part[0] == ':' {
			if t.varName != "" {
				if t.varName != part {
					panic("route: pattern conflicts with one already registered")
				}
				t = t.t[part]
				continue
			}
			t.varName = part
			if t.t == nil {
				t.t = map[string]*trie{}
			}
			t.t[part] = &trie{}
			t = t.t[part]
			continue
		}
		// Is part a *var?
		if part[0] == '*' {
			if i < len(parts)-1 {
				panic("route: suffix variables cannot contain '/'")
			}
			if t.varName != "" {
				if t.varName != part {
					panic("route: pattern conflicts with one already registered")
				}
				t = t.t[part]
				break
			}
			t.varName = part
			if t.t == nil {
				t.t = map[string]*trie{}
			}
			t.t[part] = &trie{}
			t = t.t[part]
			break
		}
		// Part is not a var.
		if _, ok := t.t[part]; !ok {
			if t.t == nil {
				t.t = map[string]*trie{}
			}
			t.t[part] = &trie{}
		}
		t = t.t[part]
	}
	if _, ok := t.verbs[method]; ok {
		panic("route: pattern conflicts with one already registered")
	}
	if t.verbs == nil {
		t.verbs = map[string]http.HandlerFunc{}
	}
	t.verbs[method] = f
	if len(name) == 1 {
		if h.pats == nil {
			h.pats = map[string]string{}
		}
		h.pats[name[0]] = pat
	}
}

func (h *Handler) Get(pat string, f http.HandlerFunc, name ...string) {
	h.Match("GET", pat, f, name...)
}

func (h *Handler) Pst(pat string, f http.HandlerFunc, name ...string) {
	h.Match("POST", pat, f, name...)
}

func (h *Handler) Put(pat string, f http.HandlerFunc, name ...string) {
	h.Match("PUT", pat, f, name...)
}

func (h *Handler) Del(pat string, f http.HandlerFunc, name ...string) {
	h.Match("DELETE", pat, f, name...)
}

func (h *Handler) Opt(pat string, f http.HandlerFunc, name ...string) {
	h.Match("OPTIONS", pat, f, name...)
}

func (h *Handler) URL(name string, args ...string) string {
	pat, ok := h.pats[name]
	if !ok {
		panic("route: there is no pattern by that name")
	}
	pat = path.Clean(pat)
	parts := []string{}
	if pat != "/" {
		parts = strings.Split(pat[1:], "/")
	}
	argi := 0
	for i, part := range parts {
		switch part[0] {
		case ':', '*':
			if argi == len(args) {
				panic("route: not enough arguments to fill in the pattern")
			}
			parts[i] = args[argi]
			argi++
		}
	}
	if argi < len(args) {
		panic("route: too many arguments to fill in the pattern")
	}
	return "/" + path.Join(parts...)
}

// ServeHTTP dispatches to the HandlerFunc whose pattern matches the
// request.
func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if h.HandlePanic != nil {
		defer func() {
			if p := recover(); p != nil {
				h.HandlePanic(r, p)
			}
		}()
	}
	p := path.Clean(r.URL.Path)
	parts := []string{}
	if p != "/" {
		parts = strings.Split(p[1:], "/")
	}
	t := &h.trie
	for i, part := range parts {
		// Try to match exactly first.
		if part[0] != ':' && part[0] != '*' {
			if t2, ok := t.t[part]; ok {
				t = t2
				continue
			}
		}
		// Try to use a variable instead.
		if t.varName == "" {
			h.handle404(w, r)
			return
		}
		if t.varName[0] == '*' {
			r.URL.RawQuery = appendQuery(r.URL.RawQuery, t.varName, strings.Join(parts[i:], "/"))
			t = t.t[t.varName]
			break
		}
		r.URL.RawQuery = appendQuery(r.URL.RawQuery, t.varName, part)
		t = t.t[t.varName]
	}
	if len(t.verbs) == 0 {
		h.handle404(w, r)
		return
	}
	f, ok := t.verbs[r.Method]
	if !ok {
		verbs := []string{}
		for k := range t.verbs {
			verbs = append(verbs, k)
		}
		h.handle405(w, r, verbs)
		return
	}
	f(w, r)
}

func (h *Handler) handle404(w http.ResponseWriter, r *http.Request) {
	if h.Handle404 != nil {
		h.Handle404(w, r)
		return
	}
	http.NotFound(w, r)
}

func (h *Handler) handle405(w http.ResponseWriter, r *http.Request, verbs []string) {
	w.Header().Set("Allow", strings.Join(verbs, ", "))
	if h.Handle405 != nil {
		h.Handle405(w, r)
		return
	}
	http.Error(w, "405 method not allowed", 405)
}

func appendQuery(query, key, value string) string {
	s := url.QueryEscape(key) + "=" + url.QueryEscape(value)
	if query == "" {
		return s
	}
	return query + "&" + s
}
