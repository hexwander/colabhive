package checkpoint

import (
	"errors"
	"testing"
)

func TestIsRemoteDirMissingErr(t *testing.T) {
	cases := []struct {
		err  error
		want bool
	}{
		{nil, false},
		{errors.New(`HTTP 404: {"message":"No such file or directory: content/checkpoints"}`), true},
		{errors.New(`HTTP 404: file or directory does not exist: content/checkpoints`), true},
		{errors.New(`directory does not exist`), true},
		{errors.New(`HTTP 404: path /content/checkpoints does not exist`), true},
		{errors.New(`model does not exist`), false},
		{errors.New(`session does not exist`), false},
		{errors.New(`network disconnect during final checkpoint`), false},
	}
	for _, tc := range cases {
		if got := isRemoteDirMissingErr(tc.err); got != tc.want {
			t.Errorf("isRemoteDirMissingErr(%v) = %v, want %v", tc.err, got, tc.want)
		}
	}
}
