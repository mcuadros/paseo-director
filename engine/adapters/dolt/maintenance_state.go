// SPDX-License-Identifier: Apache-2.0

package dolt

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"syscall"

	domainmaintenance "github.com/mcuadros/director-engine/domain/taskstoremaintenance"
)

const maximumMaintenanceStateBytes = 256 * 1024

var ErrMaintenanceUnsafe = errors.New("TaskStore maintenance storage is unsafe")

type maintenanceRootIdentity struct {
	device uint64
	inode  uint64
	uid    uint32
}

func exactOwnedDirectory(path string) (maintenanceRootIdentity, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return maintenanceRootIdentity{}, err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm() != 0o700 {
		return maintenanceRootIdentity{}, ErrMaintenanceUnsafe
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || stat.Uid != uint32(os.Geteuid()) {
		return maintenanceRootIdentity{}, ErrMaintenanceUnsafe
	}
	return maintenanceRootIdentity{device: uint64(stat.Dev), inode: stat.Ino, uid: stat.Uid}, nil
}

func exactOwnedFile(path string, maximum int64) (os.FileInfo, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm() != 0o600 ||
		stat.Uid != uint32(os.Geteuid()) || info.Size() < 0 || info.Size() > maximum {
		return nil, ErrMaintenanceUnsafe
	}
	return info, nil
}

func syncDirectory(path string) error {
	directory, err := os.Open(path)
	if err != nil {
		return ErrMaintenanceUnsafe
	}
	err = errors.Join(directory.Sync(), directory.Close())
	if err != nil {
		return ErrMaintenanceUnsafe
	}
	return nil
}

func decodeMaintenanceState(document []byte) (domainmaintenance.State, error) {
	if len(document) == 0 || len(document) > maximumMaintenanceStateBytes || !json.Valid(document) {
		return domainmaintenance.State{}, ErrMaintenanceUnsafe
	}
	decoder := json.NewDecoder(bytes.NewReader(document))
	decoder.DisallowUnknownFields()
	var state domainmaintenance.State
	if decoder.Decode(&state) != nil {
		return domainmaintenance.State{}, ErrMaintenanceUnsafe
	}
	if _, err := decoder.Token(); !errors.Is(err, io.EOF) || !domainmaintenance.ValidState(state) {
		return domainmaintenance.State{}, ErrMaintenanceUnsafe
	}
	return state, nil
}

func (maintenance *Maintenance) verifyRoot() error {
	identity, err := exactOwnedDirectory(maintenance.root)
	if err != nil || identity != maintenance.identity {
		return ErrMaintenanceUnsafe
	}
	return nil
}

func (maintenance *Maintenance) withStateLock(ctx context.Context, action func() error) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := maintenance.verifyRoot(); err != nil {
		return err
	}
	path := filepath.Join(maintenance.root, "maintenance.lock")
	file, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return ErrMaintenanceUnsafe
	}
	defer file.Close()
	if _, err := exactOwnedFile(path, 0); err != nil {
		return ErrMaintenanceUnsafe
	}
	if err := syscall.Flock(int(file.Fd()), syscall.LOCK_EX); err != nil {
		return ErrMaintenanceUnsafe
	}
	defer syscall.Flock(int(file.Fd()), syscall.LOCK_UN)
	if err := ctx.Err(); err != nil {
		return err
	}
	return action()
}

func (maintenance *Maintenance) readStateUnlocked() (domainmaintenance.State, error) {
	path := filepath.Join(maintenance.root, "maintenance-state.json")
	before, err := exactOwnedFile(path, maximumMaintenanceStateBytes)
	if err != nil {
		return domainmaintenance.State{}, ErrMaintenanceUnsafe
	}
	document, err := os.ReadFile(path)
	if err != nil {
		return domainmaintenance.State{}, ErrMaintenanceUnsafe
	}
	after, err := exactOwnedFile(path, maximumMaintenanceStateBytes)
	if err != nil || !os.SameFile(before, after) || before.Size() != after.Size() || before.ModTime() != after.ModTime() {
		return domainmaintenance.State{}, ErrMaintenanceUnsafe
	}
	state, err := decodeMaintenanceState(document)
	if err != nil || state.StoreID != maintenance.store.storeID || state.StoreBindingSHA256 != maintenance.binding {
		return domainmaintenance.State{}, ErrMaintenanceUnsafe
	}
	return state, nil
}

func (maintenance *Maintenance) writeStateUnlocked(state domainmaintenance.State) error {
	if !domainmaintenance.ValidState(state) || state.StoreID != maintenance.store.storeID ||
		state.StoreBindingSHA256 != maintenance.binding || maintenance.verifyRoot() != nil {
		return ErrMaintenanceUnsafe
	}
	document, err := json.Marshal(state)
	if err != nil || len(document) > maximumMaintenanceStateBytes {
		return ErrMaintenanceUnsafe
	}
	temporary, err := os.CreateTemp(maintenance.root, ".maintenance-state-*.tmp")
	if err != nil {
		return ErrMaintenanceUnsafe
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)
	if temporary.Chmod(0o600) != nil {
		_ = temporary.Close()
		return ErrMaintenanceUnsafe
	}
	if _, err := temporary.Write(document); err != nil || temporary.Sync() != nil || temporary.Close() != nil {
		return ErrMaintenanceUnsafe
	}
	if maintenance.verifyRoot() != nil || os.Rename(temporaryPath, filepath.Join(maintenance.root, "maintenance-state.json")) != nil {
		return ErrMaintenanceUnsafe
	}
	return syncDirectory(maintenance.root)
}

func (maintenance *Maintenance) initializeState(ctx context.Context) error {
	return maintenance.withStateLock(ctx, func() error {
		_, err := exactOwnedFile(filepath.Join(maintenance.root, "maintenance-state.json"), maximumMaintenanceStateBytes)
		if err == nil {
			_, err = maintenance.readStateUnlocked()
			return err
		}
		if !errors.Is(err, os.ErrNotExist) {
			return ErrMaintenanceUnsafe
		}
		return maintenance.writeStateUnlocked(domainmaintenance.NewState(maintenance.store.storeID, maintenance.binding))
	})
}

func (maintenance *Maintenance) Load(ctx context.Context) (domainmaintenance.State, error) {
	var state domainmaintenance.State
	err := maintenance.withStateLock(ctx, func() error {
		var err error
		state, err = maintenance.readStateUnlocked()
		return err
	})
	return state, err
}

func (maintenance *Maintenance) CompareAndSwap(ctx context.Context, expected uint64, next domainmaintenance.State) error {
	return maintenance.withStateLock(ctx, func() error {
		current, err := maintenance.readStateUnlocked()
		if err != nil {
			return err
		}
		if current.Revision != expected || next.Revision != expected+1 {
			return ErrMaintenanceUnsafe
		}
		return maintenance.writeStateUnlocked(next)
	})
}
