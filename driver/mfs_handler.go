/*
   Copyright (c) 2023 Saglabs SA. All Rights Reserved.

   Licensed under the Apache License, Version 2.0 (the "License");
   you may not use this file except in compliance with the License.
   You may obtain a copy of the License at

       http://www.apache.org/licenses/LICENSE-2.0

   Unless required by applicable law or agreed to in writing, software
   distributed under the License is distributed on an "AS IS" BASIS,
   WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
   See the License for the specific language governing permissions and
   limitations under the License.
*/

package driver

import (
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path"
	"strconv"
	"strings"

	"gopkg.in/natefinch/lumberjack.v2"
)

const (
	fsType            = "moosefs"
	newVolumeMode     = 0755
	getQuotaCmd       = "mfsgetquota"
	setQuotaCmd       = "mfssetquota"
	createSnapshotCmd = "mfsmakesnapshot"
	removeSnapshotCmd = "mfsrmsnapshot"
	// maybe configurable later
	quotaLimitType = "-L"
	quotaLimitRow  = 2

	quotaLimitCol = 3

	logsDirName    = "logs"
	volumesDirName = "volumes"

	mntDir = "/mnt"
)

// todo(ad): in future possibly add more options (mount options?)
type mfsHandler struct {
	mfsmaster      string // mfsmaster address
	mfsmaster_port int    // mfsmaster port
	rootPath       string // mfs root path
	pluginDataPath string // plugin data path (inside rootPath)
	name           string // handler name
	hostMountPath  string // host mfs mount path
}

func NewMfsHandler(mfsmaster string, mfsmaster_port int, rootPath, pluginDataPath, name string, num ...int) *mfsHandler {
	var numSufix = ""
	if len(num) == 2 {
		if num[0] == 0 && num[1] == 1 {
			numSufix = ""
		} else {
			numSufix = fmt.Sprintf("_%02d", num[0])
		}
	} else if len(num) != 0 {
		log.Errorf("NewMfsHandler - Unexpected number of arguments: %d; expected 0 or 2", len(num))
	}

	return &mfsHandler{
		mfsmaster:      mfsmaster,
		mfsmaster_port: mfsmaster_port,
		rootPath:       rootPath,
		pluginDataPath: pluginDataPath,
		name:           name,
		hostMountPath:  path.Join(mntDir, fmt.Sprintf("%s%s", name, numSufix)),
	}
}

func (mnt *mfsHandler) SetMfsLogging() {
	log.Infof("Setting up Mfs Logging. Mfs path: %s", path.Join(mnt.rootPath, mnt.pluginDataPath, logsDirName))
	mfsLogFile := &lumberjack.Logger{
		Filename:   path.Join(mnt.HostPathToLogs(), fmt.Sprintf("%s.log", mnt.name)),
		MaxSize:    100,
		MaxBackups: 3,
		MaxAge:     0,
		Compress:   true,
	}
	mw := io.MultiWriter(os.Stderr, mfsLogFile)
	log.SetOutput(mw)
	log.Info("Mfs Logging set up!")
}

func (mnt *mfsHandler) VolumeExist(volumeId string) (bool, error) {
	path := mnt.HostPathToVolume(volumeId)
	_, err := os.Stat(path)
	if err == nil {
		return true, nil
	}
	if os.IsNotExist(err) {
		return false, nil
	}
	return false, err
}

func (mnt *mfsHandler) MountVolumeExist(volumeId string) (bool, error) {
	path := mnt.HostPathToMountVolume(volumeId)
	_, err := os.Stat(path)
	if err == nil {
		return true, nil
	}
	if os.IsNotExist(err) {
		return false, nil
	}
	return false, err
}

func (mnt *mfsHandler) CreateMountVolume(volumeId string) error {
	path := mnt.HostPathToMountVolume(volumeId)
	if err := os.MkdirAll(path, newVolumeMode); err != nil {
		return err
	}
	return nil
}

func (mnt *mfsHandler) CreateVolume(volumeId string, size int64) (int64, error) {
	path := mnt.HostPathToVolume(volumeId)
	if err := os.MkdirAll(path, newVolumeMode); err != nil {
		return 0, err
	}
	if size == 0 {
		return 0, nil
	}
	acquiredSize, err := mnt.SetQuota(volumeId, size)
	if err != nil {
		return 0, err
	}
	return acquiredSize, nil
}

func (mnt *mfsHandler) DeleteVolume(volumeId string) error {
	path := mnt.HostPathToVolume(volumeId)
	if err := os.RemoveAll(path); err != nil {
		// todo(ad): fix msg
		log.Errorf("-------------------ControllerService::DeleteVolume -- Couldn't remove volume %s in directory %s. Error: %s",
			volumeId, path, err.Error())
		return err
	}

	return nil
}

func (mnt *mfsHandler) GetQuota(volumeId string) (int64, error) {
	log.Infof("GetQuota - volumeId: %s", volumeId)

	path := mnt.MfsPathToVolume(volumeId)

	cmd := exec.Command(getQuotaCmd, path)
	cmd.Dir = mnt.hostMountPath
	out, err := cmd.CombinedOutput()

	if err != nil {
		return 0, fmt.Errorf("GetQuota: Error while executing command %s %s. Error: %s output: %v", getQuotaCmd, path, err.Error(), string(out))
	}
	if quotaLimit, err := parseMfsQuotaToolsOutput(string(out)); err != nil {
		return 0, err
	} else if quotaLimit == -1 {
		return 0, fmt.Errorf("GetQuota: Quota for volume %s is not set or %s output is incorrect. Output: %s", volumeId, getQuotaCmd, string(out))
	} else {
		return quotaLimit, nil
	}
}

func (mnt *mfsHandler) SetQuota(volumeId string, size int64) (int64, error) {
	log.Infof("SetQuota - volumeId: %s, size: %d", volumeId, size)

	path := mnt.MfsPathToVolume(volumeId)
	if size <= 0 {
		return 0, errors.New("SetQuota: size must be positive")
	}
	setQuotaArgs := []string{quotaLimitType, strconv.FormatInt(size, 10), path}
	cmd := exec.Command(setQuotaCmd, setQuotaArgs...)
	cmd.Dir = mnt.hostMountPath
	out, err := cmd.CombinedOutput()

	if err != nil {
		return 0, fmt.Errorf("SetQuota: Error while executing command %s %v. Error: %s output: %v", setQuotaCmd, setQuotaArgs, err.Error(), string(out))
	}
	if quotaLimit, err := parseMfsQuotaToolsOutput(string(out)); err != nil {
		return 0, err
	} else if quotaLimit == -1 {
		return 0, fmt.Errorf("SetQuota: Quota for volume %s is not set or %s output is incorrect. Output: %s", volumeId, setQuotaCmd, string(out))
	} else {
		return quotaLimit, nil
	}
}

func (mnt *mfsHandler) CreateSnapshot(volumeId string, snapshotId string) (int64, error) {
	volPath := mnt.HostPathToVolume(volumeId)
	snapPath := mnt.HostPathToVolume(snapshotId)
	createSnapshotArgs := []string{volPath, snapPath}
	cmd := exec.Command(createSnapshotCmd, createSnapshotArgs...)
	cmd.Dir = mnt.hostMountPath
	_, err := cmd.CombinedOutput()

	if err != nil {
		return 0, err
	}

	return mnt.GetQuota(snapPath)
}

func parseMfsQuotaToolsOutput(output string) (int64, error) {
	lines := strings.Split(output, "\n")
	if len(lines) <= quotaLimitRow {
		return 0, fmt.Errorf("Error while parsing quota tool output (less rows than expected); output: %s", output)
	}
	cols := strings.Split(lines[quotaLimitRow], "|")
	if len(cols) < 5 {
		return 0, fmt.Errorf("Error while parsing quota tool output (less columns than expected); output: %s", output)
	}
	s := strings.TrimSpace(cols[quotaLimitCol])
	if s == "-" {
		return -1, nil // let caller take care of error. May be useful for mount volumes
	}
	quotaLimit, err := strconv.ParseInt(s, 10, 64)
	if err != nil {
		return 0, err
	}
	return quotaLimit, nil
}

// Mount mounts mfsclient at speciefied earlier point
func (mnt *mfsHandler) MountMfs() error {
	mounter := Mounter{}
	mountSource := fmt.Sprintf("%s:%d:%s", mnt.mfsmaster, mnt.mfsmaster_port, mnt.rootPath)
	mountOptions := make([]string, 0)

	log.Infof("MountMfs - source: %s, target: %s, options: %v", mountSource, mnt.hostMountPath, mountOptions)

	if isMounted, err := mounter.IsMounted(mnt.hostMountPath); err != nil {
		return err
	} else if isMounted {
		log.Warnf("MountMfs - Mount found in %s. Unmounting...", mnt.hostMountPath)
		if err = mounter.UMount(mnt.hostMountPath); err != nil {
			return err
		}
	}
	if err := os.RemoveAll(mnt.hostMountPath); err != nil {
		return err
	}
	if err := mounter.Mount(mountSource, mnt.hostMountPath, fsType, mountOptions...); err != nil {
		return err
	}
	log.Infof("MountMfs - Successfully mounted %s to %s", mountSource, mnt.hostMountPath)
	return nil
}

func (mnt *mfsHandler) BindMount(mfsSource string, target string, options ...string) error {
	mounter := Mounter{}
	source := mnt.HostPathTo(mfsSource)
	log.Infof("BindMount - source: %s, target: %s, options: %v", source, target, options)
	if isMounted, err := mounter.IsMounted(target); err != nil {
		return err
	} else if !isMounted {
		if err := mounter.Mount(source, target, fsType, append(options, "bind")...); err != nil {
			return err
		}
	} else {
		log.Infof("BindMount - target %s is already mounted", target)
	}
	return nil
}

func (mnt *mfsHandler) BindUMount(target string) error {
	mounter := Mounter{}
	log.Infof("BindUMount - target: %s", target)
	if mounted, err := mounter.IsMounted(target); err != nil {
		return err
	} else if mounted {
		if err := mounter.UMount(target); err != nil {
			return err
		}
	} else {
		log.Infof("BindUMount - target %s was already unmounted", target)
	}
	return nil
}

// HostPathToVolume returns absoluthe path to given volumeId on host mfsclient mountpoint
func (mnt *mfsHandler) HostPathToVolume(volumeId string) string {
	return path.Join(mnt.hostMountPath, mnt.pluginDataPath, "volumes", volumeId)
}

func (mnt *mfsHandler) HostPathToMountVolume(volumeId string) string {
	return path.Join(mnt.hostMountPath, mnt.pluginDataPath, "mount_volumes", volumeId)
}

// MfsPathToVolume
func (mnt *mfsHandler) MfsPathToVolume(volumeId string) string {
	return path.Join(mnt.pluginDataPath, "volumes", volumeId)
}

func (mnt *mfsHandler) HostPathToLogs() string {
	return path.Join(mnt.hostMountPath, mnt.pluginDataPath, logsDirName)
}

func (mnt *mfsHandler) HostPluginDataPath() string {
	return path.Join(mnt.hostMountPath, mnt.pluginDataPath)
}

func (mnt *mfsHandler) HostPathTo(to string) string {
	return path.Join(mnt.hostMountPath, to)
}
