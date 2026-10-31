package deepseek

import (
	"context"
	"errors"
	"fmt"
	"testing"
)

func TestIsAuthFailure(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want bool
	}{
		{"http 401", &APIError{HTTPStatus: 401}, true},
		{"http 403", &APIError{HTTPStatus: 403}, true},
		{"envelope 40003 invalid token", &APIError{HTTPStatus: 200, Code: 40003, Msg: "Authorization Failed (invalid token)"}, true},
		{"envelope wording: invalid token", &APIError{HTTPStatus: 200, Code: 40101, Msg: "token invalid"}, true},
		{"envelope wording: authorization failed", &APIError{HTTPStatus: 200, Code: 50001, Msg: "Authorization Failed: captcha required"}, true},
		{"wrapped 40003", fmt.Errorf("fetch pow challenge: %w", &APIError{HTTPStatus: 200, Code: 40003, Msg: "Authorization Failed (invalid token)"}), true},
		{"http 429", &APIError{HTTPStatus: 429}, false},
		{"http 500", &APIError{HTTPStatus: 500, Msg: "internal error"}, false},
		{"envelope non-auth code", &APIError{HTTPStatus: 200, Code: 40001, Msg: "bad request"}, false},
		{"network error", errors.New("connection reset by peer"), false},
		{"context deadline", context.DeadlineExceeded, false},
		{"nil", nil, false},
	}
	for _, c := range cases {
		if got := IsAuthFailure(c.err); got != c.want {
			t.Errorf("%s: IsAuthFailure(%v) = %v, want %v", c.name, c.err, got, c.want)
		}
	}
}
