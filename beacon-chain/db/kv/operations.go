package kv

import (
	"context"

	ethpb "github.com/prysmaticlabs/ethereumapis/eth/v1alpha1"
	"github.com/prysmaticlabs/go-ssz"
	bolt "go.etcd.io/bbolt"
	"go.opencensus.io/trace"
)

// VoluntaryExit retrieval by signing root.
func (kv *Store) VoluntaryExit(ctx context.Context, exitRoot [32]byte) (*ethpb.VoluntaryExit, error) {
	ctx, span := trace.StartSpan(ctx, "BeaconDB.VoluntaryExit")
	defer span.End()
	var exit *ethpb.VoluntaryExit
	err := kv.db.View(func(tx *bolt.Tx) error {
		bkt := tx.Bucket(voluntaryExitsBucket)
		enc := bkt.Get(exitRoot[:])
		if enc == nil {
			return nil
		}
		exit = &ethpb.VoluntaryExit{}
		return decode(enc, exit)
	})
	return exit, err
}

// HasVoluntaryExit verifies if a voluntary exit is stored in the db by its signing root.
func (kv *Store) HasVoluntaryExit(ctx context.Context, exitRoot [32]byte) bool {
	ctx, span := trace.StartSpan(ctx, "BeaconDB.HasVoluntaryExit")
	defer span.End()
	exists := false
	if err := kv.db.View(func(tx *bolt.Tx) error {
		bkt := tx.Bucket(voluntaryExitsBucket)
		exists = bkt.Get(exitRoot[:]) != nil
		return nil
	}); err != nil { // This view never returns an error, but we'll handle anyway for sanity.
		panic(err)
	}
	return exists
}

// SaveVoluntaryExit to the db by its signing root.
func (kv *Store) SaveVoluntaryExit(ctx context.Context, exit *ethpb.VoluntaryExit) error {
	ctx, span := trace.StartSpan(ctx, "BeaconDB.SaveVoluntaryExit")
	defer span.End()
	exitRoot, err := ssz.HashTreeRoot(exit)
	if err != nil {
		return err
	}
	enc, err := encode(exit)
	if err != nil {
		return err
	}
	return kv.db.Update(func(tx *bolt.Tx) error {
		bucket := tx.Bucket(voluntaryExitsBucket)
		return bucket.Put(exitRoot[:], enc)
	})
}

// deleteVoluntaryExit clears a voluntary exit from the db by its signing root.
func (kv *Store) deleteVoluntaryExit(ctx context.Context, exitRoot [32]byte) error {
	ctx, span := trace.StartSpan(ctx, "BeaconDB.deleteVoluntaryExit")
	defer span.End()
	return kv.db.Update(func(tx *bolt.Tx) error {
		bucket := tx.Bucket(voluntaryExitsBucket)
		return bucket.Delete(exitRoot[:])
	})
}
