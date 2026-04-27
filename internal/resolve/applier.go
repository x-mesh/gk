package resolve

import (
	"context"
	"os"

	"github.com/x-mesh/gk/internal/git"
)

// WriteResolved는 해결된 파일 내용을 디스크에 쓴다.
func WriteResolved(writeFn func(string, []byte, os.FileMode) error, path string, data []byte) error {
	if writeFn == nil {
		writeFn = os.WriteFile
	}
	return writeFn(path, data, 0o644)
}

// BackupOriginal은 원본 충돌 파일의 .orig 백업을 생성한다.
func BackupOriginal(writeFn func(string, []byte, os.FileMode) error, path string, data []byte) error {
	if writeFn == nil {
		writeFn = os.WriteFile
	}
	return writeFn(path+".orig", data, 0o644)
}

// GitAdd는 해결된 파일을 git staging area에 추가한다.
func GitAdd(ctx context.Context, runner git.Runner, path string) error {
	_, _, err := runner.Run(ctx, "add", path)
	return err
}
