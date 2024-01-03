//go:build windows
// +build windows

// The file sd_windows_test.go was adapted from github.com/Microsoft/go-winio under MIT license.

// The MIT License (MIT)

// Copyright (c) 2015 Microsoft

// Permission is hereby granted, free of charge, to any person obtaining a copy
// of this software and associated documentation files (the "Software"), to dsdl
// in the Software without restriction, including without limitation the rights
// to use, copy, modify, merge, publish, distribute, sublicense, and/or sell
// copies of the Software, and to permit persons to whom the Software is
// furnished to do so, subject to the following conditions:

// The above copyright notice and this permission notice shall be included in all
// copies or substantial portions of the Software.

// THE SOFTWARE IS PROVIDED "AS IS", WITHOUT WARRANTY OF ANY KIND, EXPRESS OR
// IMPLIED, INCLUDING BUT NOT LIMITED TO THE WARRANTIES OF MERCHANTABILITY,
// FITNESS FOR A PARTICULAR PURPOSE AND NONINFRINGEMENT. IN NO EVENT SHALL THE
// AUTHORS OR COPYRIGHT HOLDERS BE LIABLE FOR ANY CLAIM, DAMAGES OR OTHER
// LIABILITY, WHETHER IN AN ACTION OF CONTRACT, TORT OR OTHERWISE, ARISING FROM,
// OUT OF OR IN CONNECTION WITH THE SOFTWARE OR THE USE OR OTHER DEALINGS IN THE
// SOFTWARE.

package fs_test

import (
	"encoding/base64"
	"os"
	"path/filepath"
	"testing"

	"github.com/restic/restic/internal/fs"
	"github.com/restic/restic/internal/test"
)

func Test_SetGetFileSecurityDescriptors(t *testing.T) {
	tempDir := t.TempDir()
	testfilePath := filepath.Join(tempDir, "testfile.txt")
	// create temp file
	testfile, err := os.Create(testfilePath)
	if err != nil {
		t.Fatalf("failed to create temporary file: %s", err)
	}

	defer func() {
		err := testfile.Close()
		if err != nil {
			t.Logf("Error closing file %s: %v\n", testfilePath, err)
		}
	}()

	testSDs := []string{"AQAUrBQAAAAwAAAAAAAAAEwAAAABBQAAAAAABRUAAACIn1iuVqCC6sy9JqvqAwAAAQUAAAAAAAUVAAAAiJ9YrlaggurMvSarAQIAAAIAfAAEAAAAAAAkAKkAEgABBQAAAAAABRUAAACIn1iuVqCC6sy9JqvtAwAAABAUAP8BHwABAQAAAAAABRIAAAAAEBgA/wEfAAECAAAAAAAFIAAAACACAAAAECQA/wEfAAEFAAAAAAAFFQAAAIifWK5WoILqzL0mq+oDAAA=",
		"AQAUrBQAAAAwAAAA7AAAAEwAAAABBQAAAAAABRUAAAAvr7t03PyHGk2FokNHCAAAAQUAAAAAAAUVAAAAiJ9YrlaggurMvSarAQIAAAIAoAAFAAAAAAAkAP8BHwABBQAAAAAABRUAAAAvr7t03PyHGk2FokNHCAAAAAAkAKkAEgABBQAAAAAABRUAAACIn1iuVqCC6sy9JqvtAwAAABAUAP8BHwABAQAAAAAABRIAAAAAEBgA/wEfAAECAAAAAAAFIAAAACACAAAAECQA/wEfAAEFAAAAAAAFFQAAAIifWK5WoILqzL0mq+oDAAACAHQAAwAAAAKAJAC/AQIAAQUAAAAAAAUVAAAAL6+7dNz8hxpNhaJDtgQAAALAJAC/AQMAAQUAAAAAAAUVAAAAL6+7dNz8hxpNhaJDPgkAAAJAJAD/AQ8AAQUAAAAAAAUVAAAAL6+7dNz8hxpNhaJDtQQAAA==",
	}

	for _, testSD := range testSDs {
		sdBytes, err := base64.StdEncoding.DecodeString(testSD)
		if err != nil {
			t.Fatalf("Error decoding SD: %s", err)
		}
		sdInput, err := fs.SecurityDescriptorBytesToStruct(sdBytes)

		if err != nil {
			t.Fatalf("Error converting SD to struct: %s", err)
		}
		if err := fs.SetFileSecurityDescriptor(testfilePath, testSD); err != nil {
			t.Fatalf("set SD for file failed: %s", err)
		}

		var readSD string
		if readSD, err = fs.GetFileSecurityDescriptor(testfilePath); err != nil {
			t.Fatalf("get SD for file failed: %s", err)
		}
		sdBytes, err = base64.StdEncoding.DecodeString(readSD)
		if err != nil {
			t.Fatalf("Error decoding SD: %s", err)
		}
		sdOutput, err := fs.SecurityDescriptorBytesToStruct(sdBytes)

		if err != nil {
			t.Fatalf("Error converting SD to struct: %s", err)
		}

		test.Equals(t, sdInput, sdOutput, "SDs read from test file don't match for path: %s", testfilePath)

	}
}

func Test_SetGetFolderSecurityDescriptors(t *testing.T) {
	tempDir := t.TempDir()
	testfolderPath := filepath.Join(tempDir, "testfolder")
	// create temp folder
	err := os.Mkdir(testfolderPath, os.ModeDir)
	if err != nil {
		t.Fatalf("failed to create temporary file: %s", err)
	}

	testSDs := []string{"AQAUrBQAAAAwAAAAAAAAAEwAAAABBQAAAAAABRUAAACIn1iuVqCC6sy9JqvqAwAAAQUAAAAAAAUVAAAAiJ9YrlaggurMvSarAQIAAAIAfAAEAAAAAAAkAKkAEgABBQAAAAAABRUAAACIn1iuVqCC6sy9JqvtAwAAABMUAP8BHwABAQAAAAAABRIAAAAAExgA/wEfAAECAAAAAAAFIAAAACACAAAAEyQA/wEfAAEFAAAAAAAFFQAAAIifWK5WoILqzL0mq+oDAAA=",
		"AQAUrBQAAAAwAAAAAAAAAEwAAAABBQAAAAAABRUAAACIn1iuVqCC6sy9JqvqAwAAAQUAAAAAAAUVAAAAiJ9YrlaggurMvSarAQIAAAIA3AAIAAAAAAIUAKkAEgABAQAAAAAABQcAAAAAAxQAiQASAAEBAAAAAAAFBwAAAAAAJACpABIAAQUAAAAAAAUVAAAAiJ9YrlaggurMvSar7QMAAAAAJAC/ARMAAQUAAAAAAAUVAAAAiJ9YrlaggurMvSar6gMAAAALFAC/ARMAAQEAAAAAAAMAAAAAABMUAP8BHwABAQAAAAAABRIAAAAAExgA/wEfAAECAAAAAAAFIAAAACACAAAAEyQA/wEfAAEFAAAAAAAFFQAAAIifWK5WoILqzL0mq+oDAAA=",
		"AQAUrBQAAAAwAAAA7AAAAEwAAAABBQAAAAAABRUAAAAvr7t03PyHGk2FokNHCAAAAQUAAAAAAAUVAAAAiJ9YrlaggurMvSarAQIAAAIAoAAFAAAAAAAkAP8BHwABBQAAAAAABRUAAAAvr7t03PyHGk2FokNHCAAAAAAkAKkAEgABBQAAAAAABRUAAACIn1iuVqCC6sy9JqvtAwAAABMUAP8BHwABAQAAAAAABRIAAAAAExgA/wEfAAECAAAAAAAFIAAAACACAAAAEyQA/wEfAAEFAAAAAAAFFQAAAIifWK5WoILqzL0mq+oDAAACAHQAAwAAAAKAJAC/AQIAAQUAAAAAAAUVAAAAL6+7dNz8hxpNhaJDtgQAAALAJAC/AQMAAQUAAAAAAAUVAAAAL6+7dNz8hxpNhaJDPgkAAAJAJAD/AQ8AAQUAAAAAAAUVAAAAL6+7dNz8hxpNhaJDtQQAAA==",
	}

	for _, testSD := range testSDs {
		sdBytes, err := base64.StdEncoding.DecodeString(testSD)
		if err != nil {
			t.Fatalf("Error decoding SD: %s", err)
		}
		sdInput, err := fs.SecurityDescriptorBytesToStruct(sdBytes)

		if err != nil {
			t.Fatalf("Error converting SD to struct: %s", err)
		}
		if err := fs.SetFileSecurityDescriptor(testfolderPath, testSD); err != nil {
			t.Fatalf("set SD for folder failed: %s", err)
		}

		var readSD string
		if readSD, err = fs.GetFileSecurityDescriptor(testfolderPath); err != nil {
			t.Fatalf("get SD for folder failed: %s", err)
		}
		sdBytes, err = base64.StdEncoding.DecodeString(readSD)
		if err != nil {
			t.Fatalf("Error decoding SD: %s", err)
		}
		sdOutput, err := fs.SecurityDescriptorBytesToStruct(sdBytes)

		if err != nil {
			t.Fatalf("Error converting SD to struct: %s", err)
		}

		test.Equals(t, sdInput, sdOutput, "SDs read from test folder don't match for path: %s", testfolderPath)

	}
}
