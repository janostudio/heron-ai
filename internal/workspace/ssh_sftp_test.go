package workspace

import (
	"bytes"
	"errors"
	"io"
	"os"
	"testing"

	"github.com/stretchr/testify/require"
)

// fakeSFTPFile is a sftpFile whose Close (and optionally Read/Write) fails, so
// the helpers can be driven without a real SSH server.
type fakeSFTPFile struct {
	r         io.Reader
	written   bytes.Buffer
	readErr   error
	writeErr  error
	closeErr  error
	closeCall int
}

func (f *fakeSFTPFile) Read(p []byte) (int, error) {
	if f.readErr != nil {
		return 0, f.readErr
	}
	return f.r.Read(p)
}

func (f *fakeSFTPFile) Write(p []byte) (int, error) {
	if f.writeErr != nil {
		return 0, f.writeErr
	}
	return f.written.Write(p)
}

func (f *fakeSFTPFile) Close() error {
	f.closeCall++
	return f.closeErr
}

// fakeSFTPOpener implements sftpFileOpener over a single prepared file.
type fakeSFTPOpener struct {
	file    *fakeSFTPFile
	openErr error
	opened  string
}

func (o *fakeSFTPOpener) openRead(name string) (sftpFile, error) {
	o.opened = name
	if o.openErr != nil {
		return nil, o.openErr
	}
	return o.file, nil
}

func (o *fakeSFTPOpener) openWrite(name string) (sftpFile, error) {
	o.opened = name
	if o.openErr != nil {
		return nil, o.openErr
	}
	return o.file, nil
}

func TestSFTPWriteAllSuccessReturnsNil(t *testing.T) {
	f := &fakeSFTPFile{}
	o := &fakeSFTPOpener{file: f}

	require.NoError(t, writeAllRemote(o, "/root/workspace/a.txt", []byte("hello")))
	require.Equal(t, "hello", f.written.String())
	require.Equal(t, "/root/workspace/a.txt", o.opened)
	require.Equal(t, 1, f.closeCall, "file must be closed exactly once")
}

func TestSFTPWriteAllCloseErrorReachesCaller(t *testing.T) {
	// The whole point of the fix: a Close failure on the write path can mean
	// the server never flushed the data, so the caller must see an error
	// instead of a nil that says "written".
	errClose := errors.New("sftp: close: permission denied")
	f := &fakeSFTPFile{closeErr: errClose}
	o := &fakeSFTPOpener{file: f}

	err := writeAllRemote(o, "/root/workspace/a.txt", []byte("hello"))
	require.Error(t, err)
	require.ErrorIs(t, err, errClose)
	require.Equal(t, "hello", f.written.String(), "data was written before the close failed")
	require.Equal(t, 1, f.closeCall)
}

func TestSFTPWriteAllJoinsWriteAndCloseErrors(t *testing.T) {
	errWrite := errors.New("sftp: write failed")
	errClose := errors.New("sftp: close failed")
	f := &fakeSFTPFile{writeErr: errWrite, closeErr: errClose}
	o := &fakeSFTPOpener{file: f}

	err := writeAllRemote(o, "/root/workspace/a.txt", []byte("hello"))
	require.Error(t, err)
	require.ErrorIs(t, err, errWrite)
	require.ErrorIs(t, err, errClose)
	require.Equal(t, 1, f.closeCall, "close must still run when the write failed")
}

func TestSFTPWriteAllWriteErrorKeptUnwrapped(t *testing.T) {
	// With a healthy Close the error is returned verbatim: callers must not
	// see a new wrapper they cannot compare with errors.Is.
	errWrite := errors.New("sftp: write failed")
	f := &fakeSFTPFile{writeErr: errWrite}
	o := &fakeSFTPOpener{file: f}

	err := writeAllRemote(o, "/root/workspace/a.txt", []byte("hello"))
	require.Equal(t, errWrite, err)
}

func TestSFTPWriteAllOpenError(t *testing.T) {
	errOpen := errors.New("sftp: create failed")
	o := &fakeSFTPOpener{openErr: errOpen}

	require.Equal(t, errOpen, writeAllRemote(o, "/root/workspace/a.txt", []byte("x")))
}

func TestSFTPReadAllSuccessIgnoresCloseError(t *testing.T) {
	// Read path: the bytes are already in memory, so a Close failure is
	// server-side cleanup noise. Failing here would turn a successful read
	// into a failed one, so it is dropped on purpose.
	f := &fakeSFTPFile{r: bytes.NewReader([]byte("content")), closeErr: errors.New("sftp: close failed")}
	o := &fakeSFTPOpener{file: f}

	data, err := readAllRemote(o, "/root/workspace/a.txt")
	require.NoError(t, err)
	require.Equal(t, "content", string(data))
	require.Equal(t, 1, f.closeCall)
}

func TestSFTPReadAllSuccessReturnsNil(t *testing.T) {
	f := &fakeSFTPFile{r: bytes.NewReader([]byte("content"))}
	o := &fakeSFTPOpener{file: f}

	data, err := readAllRemote(o, "/root/workspace/a.txt")
	require.NoError(t, err)
	require.Equal(t, "content", string(data))
}

func TestSFTPReadAllJoinsReadAndCloseErrors(t *testing.T) {
	errRead := errors.New("sftp: read failed")
	errClose := errors.New("sftp: close failed")
	f := &fakeSFTPFile{r: bytes.NewReader([]byte("content")), readErr: errRead, closeErr: errClose}
	o := &fakeSFTPOpener{file: f}

	_, err := readAllRemote(o, "/root/workspace/a.txt")
	require.Error(t, err)
	require.ErrorIs(t, err, errRead)
	require.ErrorIs(t, err, errClose, "close error is diagnostic for why the read failed")
	require.Equal(t, 1, f.closeCall)
}

func TestSFTPReadAllReadErrorKeptUnwrapped(t *testing.T) {
	errRead := errors.New("sftp: read failed")
	f := &fakeSFTPFile{r: bytes.NewReader([]byte("content")), readErr: errRead}
	o := &fakeSFTPOpener{file: f}

	_, err := readAllRemote(o, "/root/workspace/a.txt")
	require.Equal(t, errRead, err, "callers match on os.IsNotExist; no new wrapper on the happy-Close path")
}

func TestSFTPReadAllNotExist(t *testing.T) {
	o := &fakeSFTPOpener{openErr: os.ErrNotExist}

	_, err := readAllRemote(o, "/root/workspace/missing.txt")
	require.ErrorIs(t, err, os.ErrNotExist)
}
