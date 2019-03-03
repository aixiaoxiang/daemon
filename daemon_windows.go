// Copyright 2016 The Go Authors. All rights reserved.
// Use of this source code is governed by
// license that can be found in the LICENSE file.

// Package daemon windows version
package daemon

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"golang.org/x/sys/windows/svc"
	"golang.org/x/sys/windows/svc/debug"
	"golang.org/x/sys/windows/svc/eventlog"
	"golang.org/x/sys/windows/svc/mgr"
)

var elog debug.Log

// windowsRecord - standard record (struct) for windows version of daemon package
type windowsRecord struct {
	name         string
	description  string
	dependencies []string
}

func newDaemon(name, description string, dependencies []string) (Daemon, error) {
	var err error
	elog, err = eventlog.Open(name)
	if err != nil {
		elog = nil
	}
	return &windowsRecord{name, description, dependencies}, nil
}

// Install the service
func (windows *windowsRecord) Install(args ...string) (string, error) {
	installAction := "Install " + windows.description + ":"

	execp, err := execPath()

	if err != nil {
		return installAction, err
	}

	m, err := mgr.Connect()
	if err != nil {
		return installAction, err
	}
	defer m.Disconnect()

	s, err := m.OpenService(windows.name)
	if err == nil {
		s.Close()
		return installAction, ErrAlreadyRunning
	}

	s, err = m.CreateService(windows.name, execp, mgr.Config{
		DisplayName:  windows.name,
		Description:  windows.description,
		StartType:    mgr.StartAutomatic,
		Dependencies: windows.dependencies,
	}, args...)
	if err != nil {
		return installAction, err
	}
	defer s.Close()

	// set recovery action for service
	// restart after 5 seconds for the first 3 times
	// restart after 1 minute, otherwise
	r := []mgr.RecoveryAction{
		mgr.RecoveryAction{
			Type:  mgr.ServiceRestart,
			Delay: 5000 * time.Millisecond,
		},
		mgr.RecoveryAction{
			Type:  mgr.ServiceRestart,
			Delay: 5000 * time.Millisecond,
		},
		mgr.RecoveryAction{
			Type:  mgr.ServiceRestart,
			Delay: 5000 * time.Millisecond,
		},
		mgr.RecoveryAction{
			Type:  mgr.ServiceRestart,
			Delay: 60000 * time.Millisecond,
		},
	}
	// set reset period as a day
	s.SetRecoveryActions(r, uint32(86400))

	err = eventlog.InstallAsEventCreate(windows.name, eventlog.Error|eventlog.Warning|eventlog.Info)
	if err != nil {
		s.Delete()
		return installAction, err
	}
	return installAction + " completed.", nil
}

// Remove the service
func (windows *windowsRecord) Remove() (string, error) {
	removeAction := "Removing " + windows.description + ":"

	m, err := mgr.Connect()
	if err != nil {
		return removeAction, getWindowsError(err)
	}
	defer m.Disconnect()
	s, err := m.OpenService(windows.name)
	if err != nil {
		return removeAction, getWindowsError(err)
	}
	defer s.Close()
	err = s.Delete()
	if err != nil {
		return removeAction, getWindowsError(err)
	}
	err = eventlog.Remove(windows.name)
	if err != nil {
		return removeAction, getWindowsError(err)
	}
	return removeAction + " completed.", nil
}

// Start the service
func (windows *windowsRecord) Start() (string, error) {
	startAction := "Starting " + windows.description + ":"

	m, err := mgr.Connect()
	if err != nil {
		return startAction, getWindowsError(err)
	}
	defer m.Disconnect()
	s, err := m.OpenService(windows.name)
	if err != nil {
		return startAction, getWindowsError(err)
	}
	defer s.Close()
	if err = s.Start("is", "manual-started"); err != nil {
		return startAction, getWindowsError(err)
	}
	return startAction + " completed.", nil
}

// Stop the service
func (windows *windowsRecord) Stop() (string, error) {
	stopAction := "Stopping " + windows.description + ":"
	err := controlService(windows.name, svc.Stop, svc.Stopped)
	if err != nil {
		return stopAction, getWindowsError(err)
	}
	return stopAction + " completed.", nil
}

func controlService(name string, c svc.Cmd, to svc.State) error {
	m, err := mgr.Connect()
	if err != nil {
		return err
	}
	defer m.Disconnect()
	s, err := m.OpenService(name)
	if err != nil {
		return fmt.Errorf("could not access service: %v", err)
	}
	defer s.Close()
	status, err := s.Control(c)
	if err != nil {
		return fmt.Errorf("could not send control=%d: %v", c, err)
	}
	timeout := time.Now().Add(10 * time.Second)
	for status.State != to {
		if timeout.Before(time.Now()) {
			return fmt.Errorf("timeout waiting for service to go to state=%d", to)
		}
		time.Sleep(300 * time.Millisecond)
		status, err = s.Query()
		if err != nil {
			return fmt.Errorf("could not retrieve service status: %v", err)
		}
	}
	return nil
}

// Status - Get service status
func (windows *windowsRecord) Status() (string, error) {
	m, err := mgr.Connect()
	if err != nil {
		return "Getting status:" + failed, getWindowsError(err)
	}
	defer m.Disconnect()
	s, err := m.OpenService(windows.name)
	if err != nil {
		return "Getting status:" + failed, getWindowsError(err)
	}
	defer s.Close()
	status, err := s.Query()
	if err != nil {
		return "Getting status:" + failed, getWindowsError(err)
	}

	return "Status: " + getWindowsServiceStateFromUint32(status.State), nil
}

// Get executable path
func execPath() (string, error) {
	prog := os.Args[0]
	p, err := filepath.Abs(prog)
	if err != nil {
		return "", err
	}
	fi, err := os.Stat(p)
	if err == nil {
		if !fi.Mode().IsDir() {
			return p, nil
		}
		err = fmt.Errorf("%s is directory", p)
	}
	if filepath.Ext(p) == "" {
		p += ".exe"
		fi, err := os.Stat(p)
		if err == nil {
			if !fi.Mode().IsDir() {
				return p, nil
			}
			err = fmt.Errorf("%s is directory", p)
		}
	}
	return "", err
}

// Get windows error
func getWindowsError(inputError error) error {
	if exiterr, ok := inputError.(*exec.ExitError); ok {
		if status, ok := exiterr.Sys().(syscall.WaitStatus); ok {
			if sysErr, ok := WinErrCode[status.ExitStatus()]; ok {
				return errors.New(fmt.Sprintf("\n %s: %s \n %s", sysErr.Title, sysErr.Description, sysErr.Action))
			}
		}
	}
	return inputError
}

// Get windows service state
func getWindowsServiceStateFromUint32(state svc.State) string {
	switch state {
	case svc.Stopped:
		return "SERVICE_STOPPED"
	case svc.StartPending:
		return "SERVICE_START_PENDING"
	case svc.StopPending:
		return "SERVICE_STOP_PENDING"
	case svc.Running:
		return "SERVICE_RUNNING"
	case svc.ContinuePending:
		return "SERVICE_CONTINUE_PENDING"
	case svc.PausePending:
		return "SERVICE_PAUSE_PENDING"
	case svc.Paused:
		return "SERVICE_PAUSED"
	}
	return "SERVICE_UNKNOWN"
}

type serviceHandler struct {
	executable Executable
}

func (sh *serviceHandler) Execute(args []string, r <-chan svc.ChangeRequest, changes chan<- svc.Status) (ssec bool, errno uint32) {
	const cmdsAccepted = svc.AcceptStop | svc.AcceptShutdown | svc.AcceptPauseAndContinue
	changes <- svc.Status{State: svc.StartPending}
	fasttick := time.Tick(500 * time.Millisecond)
	slowtick := time.Tick(2 * time.Second)
	tick := fasttick
	sh.executable.Start()
	changes <- svc.Status{State: svc.Running, Accepts: cmdsAccepted}
	sh.executable.Run()
	if elog != nil {
		elog.Info(1, "start-run")
	}
loop:
	for {
		select {
		case <-tick:
			// beep()
			// elog.Info(1, "beep")
		case c := <-r:
			switch c.Cmd {
			case svc.Interrogate:
				changes <- c.CurrentStatus
				// Testing deadlock from https://code.google.com/p/winsvc/issues/detail?id=4
				time.Sleep(100 * time.Millisecond)
				changes <- c.CurrentStatus
			case svc.Stop, svc.Shutdown:
				// golang.org/x/sys/windows/svc.TestExample is verifying this output.
				if elog != nil {
					testOutput := strings.Join(args, "-")
					testOutput += fmt.Sprintf("-%d", c.Context)
					elog.Info(1, testOutput)
				}
				sh.executable.Stop()
				break
				// break loop
			case svc.Pause:
				changes <- svc.Status{State: svc.Paused, Accepts: cmdsAccepted}
				tick = slowtick
			case svc.Continue:
				changes <- svc.Status{State: svc.Running, Accepts: cmdsAccepted}
				tick = fasttick
			default:
				if elog != nil {
					elog.Error(1, fmt.Sprintf("unexpected control request #%d", c))
				}
				continue loop
			}
		}
	}
	changes <- svc.Status{State: svc.StopPending}
	return
}

func (windows *windowsRecord) Run(e Executable) (string, error) {
	runAction := "Running " + windows.description + ":"

	interactive, err := svc.IsAnInteractiveSession()
	if err != nil {
		return runAction + failed, getWindowsError(err)
	}
	if !interactive {
		// service called from windows service manager
		// use API provided by golang.org/x/sys/windows
		err = svc.Run(windows.name, &serviceHandler{
			executable: e,
		})
		if err != nil {
			return runAction + failed, getWindowsError(err)
		}
	} else {
		// otherwise, service should be called from terminal session
		e.Run()
	}

	return runAction + " completed.", nil
}

// GetTemplate - gets service config template
func (linux *windowsRecord) GetTemplate() string {
	return ""
}

// SetTemplate - sets service config template
func (linux *windowsRecord) SetTemplate(tplStr string) error {
	return errors.New(fmt.Sprintf("templating is not supported for windows"))
}
