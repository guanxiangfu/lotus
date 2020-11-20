package storageadapter

import (
	"bytes"
	"context"

	"github.com/filecoin-project/go-address"
	"github.com/filecoin-project/go-fil-markets/storagemarket"
	"github.com/filecoin-project/go-state-types/abi"
	"github.com/filecoin-project/lotus/build"
	"github.com/filecoin-project/lotus/chain/actors/builtin/market"
	"github.com/filecoin-project/lotus/chain/actors/builtin/miner"
	"github.com/filecoin-project/lotus/chain/events"
	"github.com/filecoin-project/lotus/chain/types"
	"github.com/ipfs/go-cid"
	"golang.org/x/xerrors"
)

type sectorCommittedEventsAPI interface {
	Called(check events.CheckFunc, msgHnd events.MsgHandler, rev events.RevertHandler, confidence int, timeout abi.ChainEpoch, mf events.MsgMatchFunc) error
}

func OnDealSectorPreCommitted(ctx context.Context, api getCurrentDealInfoAPI, eventsApi sectorCommittedEventsAPI, provider address.Address, dealID abi.DealID, proposal market.DealProposal, publishCid *cid.Cid, cb storagemarket.DealSectorPreCommittedCallback) error {
	checkFunc := func(ts *types.TipSet) (done bool, more bool, err error) {
		newDealID, sd, err := GetCurrentDealInfo(ctx, ts, api, dealID, proposal, publishCid)
		if err != nil {
			// TODO: This may be fine for some errors
			return false, false, xerrors.Errorf("failed to look up deal on chain: %w", err)
		}
		dealID = newDealID

		// Sector has already started, bail out
		if sd.State.SectorStartEpoch > 0 {
			cb(0, nil)
			return true, false, nil
		}

		return false, true, nil
	}

	// A pre-commit message was sent to the provider.
	// Check if the message params included the deal ID we're looking for.
	called := func(msg *types.Message, rec *types.MessageReceipt, ts *types.TipSet, curH abi.ChainEpoch) (more bool, err error) {
		defer func() {
			if err != nil {
				cb(0, xerrors.Errorf("handling applied event: %w", err))
			}
		}()

		if msg == nil {
			log.Error("timed out waiting for deal pre-commit... what now?")
			return false, nil
		}

		var params miner.SectorPreCommitInfo
		if err := params.UnmarshalCBOR(bytes.NewReader(msg.Params)); err != nil {
			return false, xerrors.Errorf("unmarshal pre commit: %w", err)
		}

		dealID, _, err = GetCurrentDealInfo(ctx, ts, api, dealID, proposal, publishCid)
		if err != nil {
			return false, err
		}

		for _, did := range params.DealIDs {
			if did == dealID {
				// Found the deal ID in this message. Callback with the sector ID.
				cb(params.SectorNumber, nil)
				return false, nil
			}
		}

		return true, nil
	}

	revert := func(ctx context.Context, ts *types.TipSet) error {
		log.Warn("deal pre-commit reverted; TODO: actually handle this!")
		// TODO: Just go back to DealSealing?
		return nil
	}

	// Match pre-commit message sent to the provider
	matchEvent := func(msg *types.Message) (matched bool, err error) {
		match := msg.To == provider && msg.Method == miner.Methods.PreCommitSector
		return match, nil
	}

	if err := eventsApi.Called(checkFunc, called, revert, int(build.MessageConfidence+1), events.NoTimeout, matchEvent); err != nil {
		return xerrors.Errorf("failed to set up called handler: %w", err)
	}

	return nil
}

func OnDealSectorCommitted(ctx context.Context, api getCurrentDealInfoAPI, eventsApi sectorCommittedEventsAPI, provider address.Address, dealID abi.DealID, sectorNumber abi.SectorNumber, proposal market.DealProposal, publishCid *cid.Cid, cb storagemarket.DealSectorCommittedCallback) error {
	checkFunc := func(ts *types.TipSet) (done bool, more bool, err error) {
		newDealID, sd, err := GetCurrentDealInfo(ctx, ts, api, dealID, proposal, publishCid)
		if err != nil {
			// TODO: This may be fine for some errors
			return false, false, xerrors.Errorf("failed to look up deal on chain: %w", err)
		}
		dealID = newDealID

		// Sector has already started, bail out
		if sd.State.SectorStartEpoch > 0 {
			cb(nil)
			return true, false, nil
		}

		return false, true, nil
	}

	called := func(msg *types.Message, rec *types.MessageReceipt, ts *types.TipSet, curH abi.ChainEpoch) (more bool, err error) {
		defer func() {
			if err != nil {
				cb(xerrors.Errorf("handling applied event: %w", err))
			}
		}()

		if msg == nil {
			log.Error("timed out waiting for deal activation... what now?")
			return false, nil
		}

		_, sd, err := GetCurrentDealInfo(ctx, ts, api, dealID, proposal, publishCid)
		if err != nil {
			return false, xerrors.Errorf("failed to look up deal on chain: %w", err)
		}

		if sd.State.SectorStartEpoch < 1 {
			return false, xerrors.Errorf("deal wasn't active: deal=%d, parentState=%s, h=%d", dealID, ts.ParentState(), ts.Height())
		}

		log.Infof("Storage deal %d activated at epoch %d", dealID, sd.State.SectorStartEpoch)

		cb(nil)

		return false, nil
	}

	revert := func(ctx context.Context, ts *types.TipSet) error {
		log.Warn("deal activation reverted; TODO: actually handle this!")
		// TODO: Just go back to DealSealing?
		return nil
	}

	// Match a prove-commit sent to the provider with the given sector number
	matchEvent := func(msg *types.Message) (matched bool, err error) {
		if msg.To != provider || msg.Method != miner.Methods.ProveCommitSector {
			return false, nil
		}

		var params miner.ProveCommitSectorParams
		if err := params.UnmarshalCBOR(bytes.NewReader(msg.Params)); err != nil {
			return false, xerrors.Errorf("failed to unmarshal prove commit sector params: %w", err)
		}

		if params.SectorNumber != sectorNumber {
			return false, nil
		}

		return true, nil
	}

	if err := eventsApi.Called(checkFunc, called, revert, int(build.MessageConfidence+1), events.NoTimeout, matchEvent); err != nil {
		return xerrors.Errorf("failed to set up called handler: %w", err)
	}

	return nil
}
