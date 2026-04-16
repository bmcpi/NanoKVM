package application

const (
	// GitHub repository for release downloads.
	GitHubOwner = "tinkerbell-community"
	GitHubRepo  = "NanoKVM"

	AppDir    = "/kvmapp"
	BackupDir = "/root/old"
	CacheDir  = "/root/.kvmcache"
)

type Service struct{}

func NewService() *Service {
	return &Service{}
}
