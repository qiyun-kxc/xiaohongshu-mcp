package browser

import (
	"os"
	"path/filepath"
	"syscall"

	"github.com/sirupsen/logrus"
)

// profileLock 基于 flock(2) 的跨进程文件锁, 防止 service / cmd/login / audit
// 等多个独立进程同时打开同一个持久 UserDataDir(否则 Chrome SingletonLock 冲突).
// 仅在设置了 XHS_BROWSER_USER_DATA_DIR 时启用.
type profileLock struct {
	f *os.File
}

// lockProfileDir 对持久 profile 目录加排他锁(非阻塞). 锁文件放在 profile 外层目录,
// 避免锁文件本身被当作 profile 内容读写. 返回的 *profileLock 在 Close 时释放.
// dir 为空(临时 profile)时返回 nil, 不加锁.
func lockProfileDir(dir string) (*profileLock, error) {
	if dir == "" {
		return nil, nil
	}

	// 锁文件放在 profile 的父目录下: <parent>/.<base>.lock
	parent := filepath.Dir(filepath.Clean(dir))
	base := filepath.Base(filepath.Clean(dir))
	lockPath := filepath.Join(parent, "."+base+".lock")

	f, err := os.OpenFile(lockPath, os.O_RDWR|os.O_CREATE, 0o644)
	if err != nil {
		return nil, err
	}

	// LOCK_EX | LOCK_NB: 排他 + 非阻塞, 抢不到立即报错而非挂起
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		_ = f.Close()
		return nil, err
	}

	logrus.Infof("acquired profile lock: %s", lockPath)
	return &profileLock{f: f}, nil
}

func (p *profileLock) release() {
	if p == nil || p.f == nil {
		return
	}
	_ = syscall.Flock(int(p.f.Fd()), syscall.LOCK_UN)
	_ = p.f.Close()
}
