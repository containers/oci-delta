//go:build no_ostree

package ocidelta

import (
	"fmt"
	"log/slog"
)

// OstreeRepoDataSource is a stub for builds without ostree support.
// It satisfies the DataSource interface but all operations return errors.
type OstreeRepoDataSource struct{}

func (s *OstreeRepoDataSource) SetCurrentFile(file string) error {
	return fmt.Errorf("ostree support is not available in this build")
}

func (s *OstreeRepoDataSource) Read(data []byte) (int, error) {
	return 0, fmt.Errorf("ostree support is not available in this build")
}

func (s *OstreeRepoDataSource) Seek(offset int64, whence int) (int64, error) {
	return 0, fmt.Errorf("ostree support is not available in this build")
}

func (s *OstreeRepoDataSource) Close() error {
	return nil
}

func (s *OstreeRepoDataSource) Cleanup() error {
	return nil
}

var _ DataSource = (*OstreeRepoDataSource)(nil)

// NewOstreeRepoDataSource returns an error when built without ostree support.
func NewOstreeRepoDataSource(repoPath string, ref string, log *slog.Logger) (*OstreeRepoDataSource, error) {
	return nil, fmt.Errorf("ostree support is not available in this build (built with no_ostree tag)")
}

// ResolveOstreeDataSource returns an error when built without ostree support.
func ResolveOstreeDataSource(repoPath string, sourceConfigDigest string, log *slog.Logger) (*OstreeRepoDataSource, error) {
	return nil, fmt.Errorf("ostree support is not available in this build (built with no_ostree tag)")
}
