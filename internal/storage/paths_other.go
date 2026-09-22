//go:build !unix

package storage

import "os"

func openRegular(*os.Root, string, int) (*os.File, error) { return nil, errUnsupportedFilesystem }
func renameAt(*os.Root, string, *os.Root, string) error   { return errUnsupportedFilesystem }
func descriptorPath(*os.File) (string, error)             { return "", errUnsupportedFilesystem }
