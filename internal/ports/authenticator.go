package ports

import "net/http"

type Principal struct {
	Subject string
}

type Authenticator interface {
	http.Handler

	Authenticate(w http.ResponseWriter, r *http.Request, requestedURI string)
	Principal(r *http.Request) (Principal, error)
}
