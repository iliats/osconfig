//  Copyright 2019 Google Inc. All Rights Reserved.
//
//  Licensed under the Apache License, Version 2.0 (the "License");
//  you may not use this file except in compliance with the License.
//  You may obtain a copy of the License at
//
//      http://www.apache.org/licenses/LICENSE-2.0
//
//  Unless required by applicable law or agreed to in writing, software
//  distributed under the License is distributed on an "AS IS" BASIS,
//  WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
//  See the License for the specific language governing permissions and
//  limitations under the License.

//go:build !test
// +build !test

package ospatch

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"

	"github.com/GoogleCloudPlatform/osconfig/clog"
	"github.com/GoogleCloudPlatform/osconfig/packages"
	"github.com/GoogleCloudPlatform/osconfig/util"
)

var (
	systemctlPath       = "/bin/systemctl"
	chkconfigPath       = "/sbin/chkconfig"
	yumCronServicePath  = "/usr/lib/systemd/system/yum-cron.service"
	yumCronBinPath      = "/usr/sbin/yum-cron"
	dnfAutomaticPath    = "/usr/lib/systemd/system/dnf-automatic.timer"
	unattendedUpgPath   = "/usr/bin/unattended-upgrades"
)

// runCommand executes a command and returns combined output.
var runCommand = func(name string, args ...string) ([]byte, error) {
	return exec.Command(name, args...).CombinedOutput()
}

// disableYumCronSystemd disables yum-cron via systemctl.
func disableYumCronSystemd(ctx context.Context) error {
	out, err := runCommand(systemctlPath, "is-enabled", "yum-cron.service")
	if err != nil {
		if eerr, ok := err.(*exec.ExitError); ok {
			if eerr.ExitCode() == 1 {
				return nil
			}
		}
		return fmt.Errorf("error checking status of yum-cron: %v, out: %s", err, out)
	}

	clog.Debugf(ctx, "Disabling yum-cron")
	if out, err := runCommand(systemctlPath, "stop", "yum-cron.service"); err != nil {
		return fmt.Errorf("error stopping yum-cron: %v, out: %s", err, out)
	}
	if out, err := runCommand(systemctlPath, "disable", "yum-cron.service"); err != nil {
		return fmt.Errorf("error disabling yum-cron: %v, out: %s", err, out)
	}
	return nil
}

// disableYumCronChkconfig disables yum-cron via chkconfig.
func disableYumCronChkconfig(ctx context.Context) error {
	out, err := runCommand(chkconfigPath, "yum-cron")
	if err != nil {
		return fmt.Errorf("error checking status of yum-cron: %v, out: %s", err, out)
	}
	if bytes.Contains(out, []byte("disabled")) {
		return nil
	}

	clog.Debugf(ctx, "Disabling yum-cron")
	if out, err := runCommand(chkconfigPath, "yum-cron", "off"); err != nil {
		return fmt.Errorf("error disabling yum-cron: %v, out: %s", err, out)
	}
	return nil
}

// disableDnfAutomatic disables dnf-automatic timer.
func disableDnfAutomatic(ctx context.Context) error {
	out, err := runCommand(systemctlPath, "list-timers", "dnf-automatic.timer")
	if err != nil {
		return fmt.Errorf("error checking status of dnf-automatic: %v, out: %s", err, out)
	}
	if bytes.Contains(out, []byte("0 timers listed")) {
		return nil
	}

	clog.Debugf(ctx, "Disabling dnf-automatic")
	if out, err := runCommand(systemctlPath, "stop", "dnf-automatic.timer"); err != nil {
		return fmt.Errorf("error stopping dnf-automatic: %v, out: %s", err, out)
	}
	if out, err := runCommand(systemctlPath, "disable", "dnf-automatic.timer"); err != nil {
		return fmt.Errorf("error disabling dnf-automatic: %v, out: %s", err, out)
	}
	return nil
}

// disableUnattendedUpgrades removes the unattended-upgrades package.
func disableUnattendedUpgrades(ctx context.Context) error {
	clog.Debugf(ctx, "Removing unattended-upgrades package")
	return packages.RemoveAptPackages(ctx, []string{"unattended-upgrades"})
}

// DisableAutoUpdates disables system auto updates.
func DisableAutoUpdates(ctx context.Context) {
	if util.Exists(yumCronServicePath) {
		if err := disableYumCronSystemd(ctx); err != nil {
			clog.Errorf(ctx, "%v", err)
		}
	} else if util.Exists(yumCronBinPath) {
		if err := disableYumCronChkconfig(ctx); err != nil {
			clog.Errorf(ctx, "%v", err)
		}
	}

	if util.Exists(dnfAutomaticPath) {
		if err := disableDnfAutomatic(ctx); err != nil {
			clog.Errorf(ctx, "%v", err)
		}
	}

	if util.Exists(unattendedUpgPath) {
		if err := disableUnattendedUpgrades(ctx); err != nil {
			clog.Errorf(ctx, "%v", err)
		}
	}
}
