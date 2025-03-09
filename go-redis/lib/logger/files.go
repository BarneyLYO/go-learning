package logger

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
)

func checkNotExists(src string) bool {
	_, err := os.Stat(src)
	// os.IsNotExist(err) old way
	return errors.Is(err, fs.ErrNotExist)
}

func checkPermission(src string) bool {
	_, err := os.Stat(src)
	return errors.Is(err, fs.ErrPermission)
}

func isNotExistMkDir(src string) error {
	if notExist := checkNotExists(src); notExist {
		if err := makeDir(src); err != nil {
			return err
		}
	}
	return nil
}

func makeDir(src string) error {
	err := os.MkdirAll(src, os.ModePerm)
	if err != nil {
		return err
	}
	return nil
}

func mustOpen(filename, dir string) (*os.File, error) {
	if perm := checkPermission(dir); perm {
		return nil, fmt.Errorf("permission denied dir: %s", dir)
	}

	if err := isNotExistMkDir(dir); err != nil {
		return nil, fmt.Errorf("error during make dir %s, err: %s", dir, err)
	}

	path := dir + string(os.PathSeparator) + filename
	flag := os.O_APPEND | os.O_CREATE | os.O_RDWR
	f, err := os.OpenFile(path, flag, 0644)
	if err != nil {
		return nil, fmt.Errorf("fail to open file, err: %s", err)
	}

	return f, nil

}
