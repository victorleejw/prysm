package initialsync

import (
	"context"
	"encoding/hex"
	"fmt"
	"time"

	"github.com/paulbellamy/ratecounter"
	eth "github.com/prysmaticlabs/ethereumapis/eth/v1alpha1"
	"github.com/prysmaticlabs/prysm/beacon-chain/core/helpers"
	"github.com/prysmaticlabs/prysm/beacon-chain/core/state"
	"github.com/prysmaticlabs/prysm/beacon-chain/state/stateutil"
	p2ppb "github.com/prysmaticlabs/prysm/proto/beacon/p2p/v1"
	"github.com/prysmaticlabs/prysm/shared/bytesutil"
	"github.com/prysmaticlabs/prysm/shared/mathutil"
	"github.com/sirupsen/logrus"
)

const (
	// counterSeconds is an interval over which an average rate will be calculated.
	counterSeconds = 20
	// refreshTime defines an interval at which suitable peer is checked during 2nd phase of sync.
	refreshTime = 6 * time.Second
)

// blockReceiverFn defines block receiving function.
type blockReceiverFn func(ctx context.Context, block *eth.SignedBeaconBlock, blockRoot [32]byte) error

// Round Robin sync looks at the latest peer statuses and syncs with the highest
// finalized peer.
//
// Step 1 - Sync to finalized epoch.
// Sync with peers of lowest finalized root with epoch greater than head state.
//
// Step 2 - Sync to head from finalized epoch.
// Using the finalized root as the head_block_root and the epoch start slot
// after the finalized epoch, request blocks to head from some subset of peers
// where step = 1.
func (s *Service) roundRobinSync(genesis time.Time) error {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	defer s.chain.ClearCachedStates()
	state.SkipSlotCache.Disable()
	defer state.SkipSlotCache.Enable()

	s.counter = ratecounter.NewRateCounter(counterSeconds * time.Second)
	s.lastProcessedSlot = s.chain.HeadSlot()
	highestFinalizedSlot := helpers.StartSlot(s.highestFinalizedEpoch() + 1)
	queue := newBlocksQueue(ctx, &blocksQueueConfig{
		p2p:                 s.p2p,
		headFetcher:         s.chain,
		highestExpectedSlot: highestFinalizedSlot,
	})
	if err := queue.start(); err != nil {
		return err
	}
	blockReceiver := s.chain.ReceiveBlockInitialSync

	// Step 1 - Sync to end of finalized epoch.
	for blk := range queue.fetchedBlocks {
		if err := s.processBlock(ctx, genesis, blk, blockReceiver); err != nil {
			log.WithError(err).Info("Block is not processed")
			continue
		}
	}

	log.Debug("Synced to finalized epoch - now syncing blocks up to current head")
	if err := queue.stop(); err != nil {
		log.WithError(err).Debug("Error stopping queue")
	}

	if s.chain.HeadSlot() == helpers.SlotsSince(genesis) {
		return nil
	}

	// Step 2 - sync to head from any single peer.
	// This step might need to be improved for cases where there has been a long period since
	// finality. This step is less important than syncing to finality in terms of threat
	// mitigation. We are already convinced that we are on the correct finalized chain. Any blocks
	// we receive there after must build on the finalized chain or be considered invalid during
	// fork choice resolution / block processing.
	blocksFetcher := newBlocksFetcher(ctx, &blocksFetcherConfig{
		p2p:         s.p2p,
		headFetcher: s.chain,
	})
	_, _, pids, err := s.p2p.Peers().BestFinalized(1 /* maxPeers */, s.highestFinalizedEpoch())
	if err != nil {
		log.WithError(err).Debug("Could not determine best finalized")
	}
	for len(pids) == 0 {
		log.Info("Waiting for a suitable peer before syncing to the head of the chain")
		time.Sleep(refreshTime)
		_, _, pids, err = s.p2p.Peers().BestFinalized(1 /* maxPeers */, s.highestFinalizedEpoch())
		if err != nil {
			log.WithError(err).Debug("Could not determine best finalized")
		}
	}
	best := pids[0]

	for head := helpers.SlotsSince(genesis); s.chain.HeadSlot() < head; {
		count := mathutil.Min(
			helpers.SlotsSince(genesis)-s.chain.HeadSlot()+1, blocksFetcher.blocksPerSecond)
		req := &p2ppb.BeaconBlocksByRangeRequest{
			StartSlot: s.chain.HeadSlot() + 1,
			Count:     count,
			Step:      1,
		}
		log.WithFields(logrus.Fields{
			"req":  req,
			"peer": best.Pretty(),
		}).Debug("Sending batch block request")
		resp, err := blocksFetcher.requestBlocks(ctx, req, best)
		if err != nil {
			log.WithError(err).Error("Failed to receive blocks, exiting init sync")
			return nil
		}
		for _, blk := range resp {
			err := s.processBlock(ctx, genesis, blk, s.chain.ReceiveBlockNoPubsub)
			if err != nil {
				log.WithError(err).Error("Failed to process block, exiting init sync")
				return nil
			}
		}
		if len(resp) == 0 {
			break
		}
	}

	return nil
}

// highestFinalizedEpoch returns the absolute highest finalized epoch of all connected peers.
// Note this can be lower than our finalized epoch if we have no peers or peers that are all behind us.
func (s *Service) highestFinalizedEpoch() uint64 {
	highest := uint64(0)
	for _, pid := range s.p2p.Peers().Connected() {
		peerChainState, err := s.p2p.Peers().ChainState(pid)
		if err == nil && peerChainState != nil && peerChainState.FinalizedEpoch > highest {
			highest = peerChainState.FinalizedEpoch
		}
	}

	return highest
}

// logSyncStatus and increment block processing counter.
func (s *Service) logSyncStatus(genesis time.Time, blk *eth.BeaconBlock, blkRoot [32]byte) {
	s.counter.Incr(1)
	rate := float64(s.counter.Rate()) / counterSeconds
	if rate == 0 {
		rate = 1
	}
	timeRemaining := time.Duration(float64(helpers.SlotsSince(genesis)-blk.Slot)/rate) * time.Second
	log.WithFields(logrus.Fields{
		"peers":           len(s.p2p.Peers().Connected()),
		"blocksPerSecond": fmt.Sprintf("%.1f", rate),
	}).Infof(
		"Processing block %s %d/%d - estimated time remaining %s",
		fmt.Sprintf("0x%s...", hex.EncodeToString(blkRoot[:])[:8]),
		blk.Slot, helpers.SlotsSince(genesis), timeRemaining,
	)
}

// processBlock performs basic checks on incoming block, and triggers receiver function.
func (s *Service) processBlock(
	ctx context.Context,
	genesis time.Time,
	blk *eth.SignedBeaconBlock,
	blockReceiver blockReceiverFn,
) error {
	if blk.Block.Slot <= s.lastProcessedSlot {
		return fmt.Errorf("slot %d already processed", blk.Block.Slot)
	}
	blkRoot, err := stateutil.BlockRoot(blk.Block)
	if err != nil {
		return err
	}
	s.logSyncStatus(genesis, blk.Block, blkRoot)
	parentRoot := bytesutil.ToBytes32(blk.Block.ParentRoot)
	if !s.db.HasBlock(ctx, parentRoot) && !s.chain.HasInitSyncBlock(parentRoot) {
		return fmt.Errorf("beacon node doesn't have a block in db with root %#x", blk.Block.ParentRoot)
	}
	if err := blockReceiver(ctx, blk, blkRoot); err != nil {
		return err
	}
	s.lastProcessedSlot = blk.Block.Slot
	return nil
}
