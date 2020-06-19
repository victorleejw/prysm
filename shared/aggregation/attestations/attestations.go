package attestations

import (
	"github.com/pkg/errors"
	ethpb "github.com/prysmaticlabs/ethereumapis/eth/v1alpha1"
	stateTrie "github.com/prysmaticlabs/prysm/beacon-chain/state"
	"github.com/prysmaticlabs/prysm/shared/aggregation"
	"github.com/prysmaticlabs/prysm/shared/bls"
	"github.com/prysmaticlabs/prysm/shared/featureconfig"
	"github.com/sirupsen/logrus"
)

const (
	// NaiveAggregation is an aggregation strategy without any optimizations.
	NaiveAggregation AttestationAggregationStrategy = "naive"

	// MaxCoverAggregation is a strategy based on Maximum Coverage greedy algorithm.
	MaxCoverAggregation AttestationAggregationStrategy = "max_cover"
)

// BLS aggregate signature aliases for testing / benchmark substitution. These methods are
// significantly more expensive than the inner logic of AggregateAttestations so they must be
// substituted for benchmarks which analyze AggregateAttestations.
var aggregateSignatures = bls.AggregateSignatures
var signatureFromBytes = bls.SignatureFromBytes

var log = logrus.WithField("prefix", "aggregation.attestations")

// ErrInvalidAttestationCount is returned when insufficient number
// of attestations is provided for aggregation.
var ErrInvalidAttestationCount = errors.New("invalid number of attestations")

// AttestationAggregationStrategy defines attestation aggregation strategy.
type AttestationAggregationStrategy string

// selectedAggregateFn holds reference to currently selected aggregation strategy.
var selectedAggregateFn func(atts []*ethpb.Attestation) ([]*ethpb.Attestation, error)

// Aggregate aggregates attestations. The minimal number of attestations is returned.
func Aggregate(atts []*ethpb.Attestation) ([]*ethpb.Attestation, error) {
	// Setup aggregation function once.
	if selectedAggregateFn == nil {
		strategy := AttestationAggregationStrategy(featureconfig.Get().AttestationAggregationStrategy)
		switch strategy {
		case "", NaiveAggregation:
			selectedAggregateFn = NaiveAttestationAggregation
		case MaxCoverAggregation:
			selectedAggregateFn = MaxCoverAttestationAggregation
		default:
			return nil, errors.Wrapf(aggregation.ErrInvalidStrategy, "%q", strategy)
		}
	}
	return selectedAggregateFn(atts)
}

// AggregatePair aggregates pair of attestations a1 and a2 together.
func AggregatePair(a1 *ethpb.Attestation, a2 *ethpb.Attestation) (*ethpb.Attestation, error) {
	if a1.AggregationBits.Len() != a2.AggregationBits.Len() {
		return nil, aggregation.ErrBitsDifferentLen
	}
	if a1.AggregationBits.Overlaps(a2.AggregationBits) {
		return nil, aggregation.ErrBitsOverlap
	}

	baseAtt := stateTrie.CopyAttestation(a1)
	newAtt := stateTrie.CopyAttestation(a2)
	if newAtt.AggregationBits.Count() > baseAtt.AggregationBits.Count() {
		baseAtt, newAtt = newAtt, baseAtt
	}

	if baseAtt.AggregationBits.Contains(newAtt.AggregationBits) {
		return baseAtt, nil
	}

	newBits := baseAtt.AggregationBits.Or(newAtt.AggregationBits)
	newSig, err := signatureFromBytes(newAtt.Signature)
	if err != nil {
		return nil, err
	}
	baseSig, err := signatureFromBytes(baseAtt.Signature)
	if err != nil {
		return nil, err
	}

	aggregatedSig := aggregateSignatures([]*bls.Signature{baseSig, newSig})
	baseAtt.Signature = aggregatedSig.Marshal()
	baseAtt.AggregationBits = newBits

	return baseAtt, nil
}
