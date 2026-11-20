package busstats

import (
	"bytes"
	"errors"
)

// CheckDocument is an access/content guard for a UTF-8 official-document copy.
// Passing only means readable content, never that API endpoints are verified.
func CheckDocument(httpStatus int, utf8Body []byte) error {
	if httpStatus < 200 || httpStatus >= 300 {
		return errors.New("공식 문서 HTTP 접근 실패")
	}
	b := bytes.TrimSpace(utf8Body)
	if len(b) == 0 {
		return errors.New("공식 문서가 비어 있습니다")
	}
	if bytes.Contains(b, []byte("보안 정책에 의해 차단")) {
		return errors.New("공식 문서가 보안 정책 차단 화면입니다")
	}
	return nil
}
