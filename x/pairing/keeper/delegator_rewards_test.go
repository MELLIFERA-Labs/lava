package keeper_test

import (
	"testing"
	"time"

	"cosmossdk.io/math"
	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/lavanet/lava/v5/testutil/common"
	testkeeper "github.com/lavanet/lava/v5/testutil/keeper"
	"github.com/lavanet/lava/v5/utils/sigs"
	dualstakingtypes "github.com/lavanet/lava/v5/x/dualstaking/types"
	"github.com/lavanet/lava/v5/x/pairing/types"
	subscriptiontypes "github.com/lavanet/lava/v5/x/subscription/types"
	"github.com/stretchr/testify/require"
)

const (
	exactConst = "exact"
)

// TestProviderDelegatorsRewards tests that the provider's reward (considering delegations) is as expected
// Also, it checks that the delegator reward map is updated as expected
func TestProviderDelegatorsRewards(t *testing.T) {
	// define tests - all the combinations of below:
	//   commission = {0, 47, 100}
	//   limit = {0, <totalDelegations, >totalDelegations}
	//   delegation_ratio = {0/100, 50/50, 14/86}
	tests := []struct {
		name           string
		commission     uint64
		limit          int64
		d1Amount       int64  // delegator1 delegation percentage (of provider stake(=total delegations))
		d2Amount       int64  // delegator2 delegation percentage (of provider stake(=total delegations))
		providerReward uint64 // expected provider's part of the total reward
		d1Reward       int64  // expected delegator1's part of the total reward
		d2Reward       int64  // expected delegator2's part of the total reward
	}{
		{"Test #7", 0, 150, 0, 100, 50, 0, 50},
		{"Test #8", 0, 150, 50, 50, 50, 25, 25},
		{"Test #9", 0, 150, 14, 86, 50, 7, 43},
		{"Test #16", 47, 150, 0, 100, 73, 0, 27},
		{"Test #17", 47, 150, 50, 50, 74, 13, 13},
		{"Test #18", 47, 150, 14, 86, 74, 3, 23},
		{"Test #25", 100, 150, 0, 100, 100, 0, 0},
		{"Test #26", 100, 150, 50, 50, 100, 0, 0},
		{"Test #27", 100, 150, 14, 86, 100, 0, 0},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ts := newTester(t)
			ts.setupForPayments(1, 1, 1)                   // 1 provider, 1 client, 1 providersToPair
			ts.AddAccount(common.CONSUMER, 1, testBalance) // add delegator1
			ts.AddAccount(common.CONSUMER, 2, testBalance) // add delegator2

			providerAcc, provider := ts.GetAccount(common.PROVIDER, 0)
			clientAcc, client := ts.GetAccount(common.CONSUMER, 0)
			_, delegator1 := ts.GetAccount(common.CONSUMER, 1)
			_, delegator2 := ts.GetAccount(common.CONSUMER, 2)

			_, err := ts.TxSubscriptionBuy(client, client, "free", 1, false, false) // extend by a month so the sub won't expire
			require.NoError(t, err)

			ts.AdvanceEpoch() // to apply pairing

			// ** check provider reward without delegators ** //

			relayPaymentMessage := sendRelay(ts, provider, clientAcc, []string{ts.spec.Index})
			ts.payAndVerifyBalance(relayPaymentMessage, clientAcc.Addr, providerAcc.Vault.Addr, true, true, 100)

			// ** check provider reward with delegators ** //

			delegationAmount := testStake
			// delegate
			amount1 := sdk.NewCoin(ts.TokenDenom(), sdk.NewInt(tt.d1Amount*delegationAmount/100))
			if amount1.IsZero() {
				amount1.Amount = sdk.OneInt()
			}
			_, err = ts.TxDualstakingDelegate(delegator1, provider, amount1)
			require.NoError(t, err)

			amount2 := sdk.NewCoin(ts.TokenDenom(), sdk.NewInt(tt.d2Amount*delegationAmount/100))
			_, err = ts.TxDualstakingDelegate(delegator2, provider, amount2)
			require.NoError(t, err)
			ts.AdvanceEpoch() // apply delegations

			// change delegation traits of stake entry and get the modified one
			metadata, err := ts.Keepers.Epochstorage.GetMetadata(ts.Ctx, provider)
			require.NoError(t, err)
			metadata.DelegateCommission = tt.commission
			ts.Keepers.Epochstorage.SetMetadata(ts.Ctx, metadata)

			ts.AdvanceEpoch()
			stakeEntry, found := ts.Keepers.Epochstorage.GetStakeEntryCurrent(ts.Ctx, ts.spec.Index, provider)
			require.True(t, found)

			// check that there are two delegators
			res, err := ts.QueryDualstakingProviderDelegators(provider)
			require.NoError(t, err)
			require.Equal(t, 3, len(res.Delegations))

			// calc useful consts
			totalReward := sdk.NewDec(int64(relayCuSum))

			// send relay and check provider balance according to expected providerRewardPerc (done inside payAndVerifyBalance)
			relayPaymentMessage = sendRelay(ts, provider, clientAcc, []string{ts.spec.Index})
			ts.payAndVerifyBalance(relayPaymentMessage, clientAcc.Addr, providerAcc.Vault.Addr, true, true, tt.providerReward)

			// Get the delegator rewards from the delegatorRewardsMap
			resRewards1, err := ts.QueryDualstakingDelegatorRewards(delegator1, provider, stakeEntry.Chain)
			require.NoError(t, err)
			resRewards2, err := ts.QueryDualstakingDelegatorRewards(delegator2, provider, stakeEntry.Chain)
			require.NoError(t, err)

			d1Reward := int64(0)
			if tt.d1Reward == 0 {
				require.Len(t, resRewards1.Rewards, 0)
			} else {
				d1Reward = resRewards1.Rewards[0].Amount.AmountOf(ts.BondDenom()).Int64()
				require.Equal(t, tt.d1Reward, d1Reward)
				// claim delegator rewards and verify balance
				delegator1Addr, err := sdk.AccAddressFromBech32(delegator1)
				require.NoError(t, err)
				claimRewardsAndVerifyBalance(ts, delegator1Addr, provider, stakeEntry.Chain)
			}

			d2Reward := int64(0)
			if tt.d2Reward == 0 {
				require.Len(t, resRewards2.Rewards, 0)
			} else {
				d2Reward = resRewards2.Rewards[0].Amount.AmountOf(ts.BondDenom()).Int64()
				require.Equal(t, tt.d2Reward, d2Reward)
				// claim delegator rewards and verify balance
				delegator2Addr, err := sdk.AccAddressFromBech32(delegator2)
				require.NoError(t, err)
				claimRewardsAndVerifyBalance(ts, delegator2Addr, provider, stakeEntry.Chain)
			}

			// check the delegators rewards sum up to the expected total
			totalDelegatorsReward := totalReward.TruncateInt64() * (100 - int64(tt.providerReward)) / 100
			require.Equal(t, totalDelegatorsReward, d1Reward+d2Reward)
		})
	}
}

var (
	sessionID  uint64
	relayCuSum = uint64(100)
)

func sendRelay(ts *tester, provider string, clientAcc sigs.Account, chainIDs []string) types.MsgRelayPayment {
	var relays []*types.RelaySession
	epoch := int64(ts.EpochStart(ts.BlockHeight()))

	// Create relay request. Change session ID each call to avoid double spending error
	for i, chainID := range chainIDs {
		relaySession := &types.RelaySession{
			Provider:    provider,
			ContentHash: []byte(ts.spec.ApiCollections[0].Apis[0].Name),
			SessionId:   sessionID,
			SpecId:      chainID,
			CuSum:       relayCuSum,
			Epoch:       epoch,
			RelayNum:    uint64(i),
		}
		sessionID += 1

		// Sign and send the payment requests
		sig, err := sigs.Sign(clientAcc.SK, *relaySession)
		relaySession.Sig = sig
		require.Nil(ts.T, err)

		relays = append(relays, relaySession)
	}

	return types.MsgRelayPayment{Creator: provider, Relays: relays}
}

func TestPartialMonthDelegation(t *testing.T) {
	testkeeper.SetFixedTime()
	ts := newTester(t)
	ts.setupForPayments(0, 1, 1)                   // 1 client, 1 providersToPair
	ts.AddAccount(common.CONSUMER, 1, testBalance) // add delegator1
	ts.AddAccount(common.CONSUMER, 2, testBalance) // add delegator2

	clientAcc, client := ts.GetAccount(common.CONSUMER, 0)
	_, delegator1 := ts.GetAccount(common.CONSUMER, 1)

	_, err := ts.TxSubscriptionBuy(client, client, "free", 1, false, false) // extend by a month so the sub won't expire
	require.NoError(t, err)

	ts.AdvanceTimeHours(time.Hour*24*15 + time.Hour*41) // results in 15 days of the provider being active (41 hours until subscription triggers payout)

	// add another provider after 15 days
	err = ts.addProvider(1)
	require.Nil(ts.T, err)
	providerAcc, provider := ts.GetAccount(common.PROVIDER, 0)
	require.NotNil(t, providerAcc)
	metadata, err := ts.Keepers.Epochstorage.GetMetadata(ts.Ctx, provider)
	require.NoError(t, err)
	metadata.DelegateCommission = 50 // 50% commission
	ts.Keepers.Epochstorage.SetMetadata(ts.Ctx, metadata)

	ts.AdvanceTimeHours(time.Hour * 24 * 5) // 5 days passed from the provider stake, total 20 days

	stakeEntryResp, err := ts.Keepers.Pairing.Provider(ts.Ctx, &types.QueryProviderRequest{
		Address: provider,
		ChainID: ts.spec.Index,
	})
	require.Nil(t, err)
	stakeEntry := stakeEntryResp.StakeEntries[0]

	delegationAmount1 := sdk.NewCoin(ts.TokenDenom(), sdk.NewInt(testStake*3/2)) // provider did 100000 for 15 days delegator will have 150000 for 10 days so they are equal
	_, err = ts.TxDualstakingDelegate(delegator1, provider, delegationAmount1)
	require.NoError(t, err)

	res, err := ts.QueryDualstakingProviderDelegators(provider)
	require.NoError(t, err)
	require.Equal(t, 2, len(res.Delegations))
	require.Equal(t, stakeEntry.DelegateCommission, metadata.DelegateCommission)

	relayPaymentMessage := sendRelay(ts, provider, clientAcc, []string{ts.spec.Index})
	relayPaymentMessage.DescriptionString = exactConst
	// we have a provider that staked after 15/30 days, and a delegator that staked after 20/30 days
	// provider has 100k stake for 15 days, delegator has 150k stake for 10 days so they should divide half half, and the provider commission is 50%
	// 0.5 * reward (due to stake amount) + 0.5 * 0.5 * reward (half the delegator reward due to 50% commission) = 75%
	ts.payAndVerifyBalance(relayPaymentMessage, clientAcc.Addr, providerAcc.Vault.Addr, true, true, 75)
}

func TestProviderRewardWithCommission(t *testing.T) {
	ts := newTester(t)
	ts.setupForPayments(1, 1, 1)                   // 1 provider, 1 client, 1 providersToPair
	ts.AddAccount(common.CONSUMER, 1, testBalance) // add delegator1
	ts.AddAccount(common.CONSUMER, 2, testBalance) // add delegator2

	providerAcc, provider := ts.GetAccount(common.PROVIDER, 0)
	clientAcc, client := ts.GetAccount(common.CONSUMER, 0)
	delegator1Acc, delegator1 := ts.GetAccount(common.CONSUMER, 1)

	_, err := ts.TxSubscriptionBuy(client, client, "free", 1, false, false) // extend by a month so the sub won't expire
	require.NoError(t, err)

	ts.AdvanceEpoch() // to apply pairing

	delegationAmount1 := sdk.NewCoin(ts.TokenDenom(), sdk.NewInt(testStake))
	_, err = ts.TxDualstakingDelegate(delegator1, provider, delegationAmount1)
	require.NoError(t, err)
	ts.AdvanceEpoch() // apply delegations

	// ** provider's commission is 100% ** //
	metadata, err := ts.Keepers.Epochstorage.GetMetadata(ts.Ctx, provider)
	require.NoError(t, err)
	metadata.DelegateCommission = 100
	ts.Keepers.Epochstorage.SetMetadata(ts.Ctx, metadata)

	ts.AdvanceEpoch()
	stakeEntryResp, err := ts.Keepers.Pairing.Provider(ts.Ctx, &types.QueryProviderRequest{
		Address: provider,
		ChainID: ts.spec.Index,
	})
	require.Nil(t, err)
	stakeEntry := stakeEntryResp.StakeEntries[0]
	res, err := ts.QueryDualstakingProviderDelegators(provider)
	require.NoError(t, err)
	require.Equal(t, 2, len(res.Delegations))

	// the expected reward for the provider with 100% commission is the total rewards (delegators get nothing)
	totalReward := sdk.NewCoins(sdk.NewCoin(ts.TokenDenom(), math.NewInt(int64(relayCuSum))))

	totalDelegations := sdk.ZeroInt()
	var selfdelegation dualstakingtypes.Delegation
	monthTimeCtx := ts.Ctx.WithBlockTime(ts.Ctx.BlockTime().Add(time.Hour * 24 * 30)) // do the calculation for a month so delegation numbers are dividing nicely and don't give a truncation
	for _, d := range res.Delegations {
		d.Amount = ts.Keepers.Dualstaking.CalculateMonthlyCredit(monthTimeCtx, d)
		if d.Delegator == stakeEntry.Vault {
			selfdelegation = d
		} else {
			totalDelegations = totalDelegations.Add(d.Amount.Amount)
		}
	}

	providerReward, _ := ts.Keepers.Dualstaking.CalcRewards(monthTimeCtx, totalReward, totalDelegations, selfdelegation, stakeEntry.DelegateCommission)
	require.True(t, totalReward.IsEqual(providerReward), "total %v vs provider %v", totalReward, providerReward)

	// check that the expected reward equals to the provider's new balance minus old balance
	relayPaymentMessage := sendRelay(ts, provider, clientAcc, []string{ts.spec.Index})
	ts.payAndVerifyBalance(relayPaymentMessage, clientAcc.Addr, providerAcc.Vault.Addr, true, true, 100)

	// the delegator should get no rewards
	resRewards, err := ts.QueryDualstakingDelegatorRewards(delegator1, "", "")
	require.NoError(t, err)
	require.Equal(t, 0, len(resRewards.Rewards))

	// ** provider's commission is 0% ** //
	metadata, err1 := ts.Keepers.Epochstorage.GetMetadata(ts.Ctx, provider)
	require.NoError(t, err1)
	metadata.DelegateCommission = 0
	ts.Keepers.Epochstorage.SetMetadata(ts.Ctx, metadata)

	ts.AdvanceEpoch()

	// the expected reward for the provider with 0% commission is half of the total rewards
	// (in this test specifically, effectiveDelegations = delegateTotal = providerStake)
	relayPaymentMessage = sendRelay(ts, provider, clientAcc, []string{ts.spec.Index})
	ts.payAndVerifyBalance(relayPaymentMessage, clientAcc.Addr, providerAcc.Vault.Addr, true, true, 50)

	// the delegator should get the total rewards
	resRewards, err = ts.QueryDualstakingDelegatorRewards(delegator1, "", "")
	require.NoError(t, err)
	require.Equal(t, 1, len(resRewards.Rewards))
	dReward := resRewards.Rewards[0]
	expectedDRewardForRelay := totalReward
	require.Equal(t, expectedDRewardForRelay.AmountOf(ts.BondDenom()).Int64()/2, dReward.Amount.AmountOf(ts.BondDenom()).Int64())

	// claim delegator rewards and verify balance
	claimRewardsAndVerifyBalance(ts, delegator1Acc.Addr, provider, ts.spec.Index)
}

func claimRewardsAndVerifyBalance(ts *tester, delegator sdk.AccAddress, provider string, chainID string) {
	balance := ts.GetBalance(delegator)

	res, err := ts.QueryDualstakingDelegatorRewards(delegator.String(), provider, chainID)
	require.Nil(ts.T, err)
	reward := res.Rewards[0]

	_, err = ts.Servers.DualstakingServer.ClaimRewards(ts.Ctx, dualstakingtypes.NewMsgClaimRewards(delegator.String(), provider))
	require.Nil(ts.T, err)

	newBalance := ts.GetBalance(delegator)
	require.Equal(ts.T, balance+reward.Amount.AmountOf(ts.BondDenom()).Int64(), newBalance)

	res, err = ts.QueryDualstakingDelegatorRewards(delegator.String(), provider, chainID)
	require.Nil(ts.T, err)
	require.Equal(ts.T, 0, len(res.Rewards))
}

func TestQueryDelegatorRewards(t *testing.T) {
	ts := newTester(t)
	ts.setupForPayments(3, 1, 3)                   // 3 providers, 1 client, 3 providersToPair
	ts.AddAccount(common.CONSUMER, 1, testBalance) // add delegator1
	ts.AddAccount(common.CONSUMER, 2, testBalance) // add delegator2

	provider1Acc, provider1 := ts.GetAccount(common.PROVIDER, 0) // will have stake in "mock" and "mock1"
	provider2Acc, provider2 := ts.GetAccount(common.PROVIDER, 1) // will have stake in "mock"
	_, provider3 := ts.GetAccount(common.PROVIDER, 2)            // no one delegates to it
	client1Acc, client := ts.GetAccount(common.CONSUMER, 0)
	_, delegator1 := ts.GetAccount(common.CONSUMER, 1)
	_, delegator2 := ts.GetAccount(common.CONSUMER, 2) // delegates to no one

	sub, err := ts.QuerySubscriptionCurrent(client)
	require.NoError(t, err)
	currentDurationLeft := sub.Sub.DurationLeft
	require.NotZero(t, currentDurationLeft)

	monthsToBuy := 2
	_, err = ts.TxSubscriptionBuy(client, client, ts.plan.Index, monthsToBuy, false, false) // extend by a month so the sub won't expire
	require.NoError(t, err)

	sub, err = ts.QuerySubscriptionCurrent(client)
	require.NoError(t, err)
	require.Equal(t, currentDurationLeft+uint64(monthsToBuy), sub.Sub.DurationLeft)

	spec1 := common.CreateMockSpec()
	spec1.Index = "mock1"
	spec1.Name = "mock1"
	ts.AddSpec(spec1.Index, spec1)
	err = ts.StakeProvider(provider1Acc.GetVaultAddr(), provider1, spec1, testStake)
	require.NoError(t, err)

	ts.AdvanceEpoch()

	makeProviderCommissionZero(ts, provider1)
	makeProviderCommissionZero(ts, provider1)
	makeProviderCommissionZero(ts, provider2)

	delegationAmount := sdk.NewCoin(ts.TokenDenom(), sdk.NewInt(testStake))
	_, err = ts.TxDualstakingDelegate(delegator1, provider1, delegationAmount)
	require.NoError(t, err)
	_, err = ts.TxDualstakingDelegate(delegator1, provider1, delegationAmount)
	require.NoError(t, err)
	_, err = ts.TxDualstakingDelegate(delegator1, provider2, delegationAmount)
	require.NoError(t, err)

	ts.AdvanceEpoch() // apply delegations

	relayPaymentMessage := sendRelay(ts, provider1, client1Acc, []string{ts.spec.Index, spec1.Index})
	ts.payAndVerifyBalance(relayPaymentMessage, client1Acc.Addr, provider1Acc.Vault.Addr, true, true, 50)

	relayPaymentMessage = sendRelay(ts, provider2, client1Acc, []string{ts.spec.Index})
	ts.payAndVerifyBalance(relayPaymentMessage, client1Acc.Addr, provider2Acc.Vault.Addr, true, true, 50)

	planPrice := ts.plan.Price.Amount.Int64()

	// expected rewards explanation:
	// total_reward = plan_price * (cu_per_provider_per_chain / cu_used_in_sub)
	// current situation:
	// provider1: 100CU for "spec", 100CU for "spec1"; provider2: 100CU for spec
	// tracked_cu_list = [[p1, spec, 100], [p2, spec, 100], [p1, spec1, 100]]
	// recall that delegators get 50% of reward (hence dividing by 2)
	// note that tracker CU is reset between payAndVerifyBalance calls so cu_used_in_sub is 100 on the third case
	tests := []struct {
		name            string
		delegator       string
		provider        string
		chainID         string
		expectedRewards int64
	}{
		{"assigned provider+chainID", delegator1, provider1, ts.spec.Index, 2 * (planPrice * 100) / (200 * 2)},
		{"assigned provider", delegator1, provider1, "", 2 * ((planPrice * 100) / (200 * 2))},
		{"nothing assigned", delegator1, "", "", (2 * ((planPrice * 100) / (200 * 2))) + (planPrice*100)/(100*2)},
		{"invalid delegator", delegator2, provider2, spec1.Index, 0},
		{"invalid provider", delegator1, provider3, ts.spec.Index, 0},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			res, err := ts.QueryDualstakingDelegatorRewards(tt.delegator, tt.provider, tt.chainID)
			require.NoError(t, err)
			delegatorReward := int64(0)
			for _, reward := range res.Rewards {
				delegatorReward += reward.Amount.AmountOf(ts.BondDenom()).Int64()
			}
			require.Equal(t, tt.expectedRewards, delegatorReward)
		})
	}
}

// TestVaultProviderDelegatorRewardsQuery works as expected for a vault and provider addresses
// When using the delegator-rewards query, it should only accept the vault as a delegator
// and the provider as the delegation's provider (as done while staking a new provider)
func TestVaultProviderDelegatorRewardsQuery(t *testing.T) {
	ts := newTester(t)
	ts.setupForPayments(1, 1, 0) // 1 providers

	provider1Acc, provider := ts.GetAccount(common.PROVIDER, 0)
	vault := provider1Acc.GetVaultAddr()
	consumerAcc, _ := ts.GetAccount(common.CONSUMER, 0)

	err := ts.StakeProvider(vault, provider, ts.spec, testBalance)
	require.NoError(t, err)

	ts.AdvanceEpoch()

	// send relay so provider will be eligible for bonus rewards
	msg := ts.SendRelay(provider, consumerAcc, []string{ts.spec.Index}, 100)
	_, err = ts.TxPairingRelayPayment(msg.Creator, msg.Relays...)
	require.NoError(t, err)

	// advance two months so provider bonus reward will aggregate
	ts.AdvanceMonths(1)
	ts.AdvanceBlocks(ts.BlocksToSave() + 1)
	ts.AdvanceMonths(1)
	ts.AdvanceEpoch()

	tests := []struct {
		name      string
		delegator string
		provider  string
		valid     bool
	}{
		{"delegator=vault, provider=provider", vault, provider, true},
		{"delegator=provider, provider=provider", provider, provider, false},
		{"delegator=vault, provider=vault", vault, vault, false},
		{"delegator=provider, provider=vault", provider, vault, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			res, err := ts.QueryDualstakingDelegatorRewards(tt.delegator, tt.provider, ts.spec.Index)
			require.NoError(t, err)
			if tt.valid {
				require.Len(t, res.Rewards, 1)
			} else {
				require.Len(t, res.Rewards, 0)
			}
		})
	}
}

func makeProviderCommissionZero(ts *tester, provider string) {
	metadata, err := ts.Keepers.Epochstorage.GetMetadata(ts.Ctx, provider)
	if err != nil {
		panic(err)
	}
	metadata.DelegateCommission = 0
	ts.Keepers.Epochstorage.SetMetadata(ts.Ctx, metadata)
	ts.AdvanceEpoch()
}

// TestDelegationTimestamp checks that a new delegation get the UNIX timestamp
// of current block time (UTC). Also, the timestamp should not change when delegating more
func TestDelegationTimestamp(t *testing.T) {
	testkeeper.SetFixedTime()
	ts := newTester(t)
	ts.setupForPayments(1, 1, 2)                   // 1 provider, 1 client, 1 providersToPair
	ts.AddAccount(common.CONSUMER, 1, testBalance) // add delegator1

	_, provider := ts.GetAccount(common.PROVIDER, 0)
	_, delegator := ts.GetAccount(common.CONSUMER, 1)

	_, err := ts.TxDualstakingDelegate(delegator, provider, sdk.NewCoin(ts.TokenDenom(), sdk.NewInt(testStake)))
	delegationTime := ts.BlockTime().UTC().Unix()
	require.NoError(t, err)
	ts.AdvanceEpoch() // apply delegations

	res, err := ts.QueryDualstakingProviderDelegators(provider)
	require.NoError(t, err)
	require.Equal(t, 2, len(res.Delegations)) // expect two because of provider self delegation + delegator
	for _, d := range res.Delegations {
		if d.Delegator == delegator {
			require.Equal(t, delegationTime, d.Timestamp)
		}
	}

	// advance time
	ts.AdvanceMonths(1)
	// verify that the timestamp hasn't changed
	res, err = ts.QueryDualstakingProviderDelegators(provider)
	require.NoError(t, err)
	require.Equal(t, 2, len(res.Delegations)) // expect two because of provider self delegation + delegator
	for _, d := range res.Delegations {
		if d.Delegator == delegator {
			require.Equal(t, delegationTime, d.Timestamp)
		}
	}
	// verify that the timestamp changes when delegating more and credit is a month ago
	expectedDelegation := sdk.NewCoin(ts.TokenDenom(), sdk.NewInt(2*testStake))
	delegationTimeU := ts.BlockTime().UTC()
	_, err = ts.TxDualstakingDelegate(delegator, provider, sdk.NewCoin(ts.TokenDenom(), sdk.NewInt(testStake)))
	require.NoError(t, err)
	ts.AdvanceEpoch() // apply delegations

	res, err = ts.QueryDualstakingProviderDelegators(provider)
	require.NoError(t, err)
	require.Equal(t, 2, len(res.Delegations)) // expect two because of provider self delegation + delegator
	for _, d := range res.Delegations {
		if d.Delegator == delegator {
			creditStart := delegationTimeU.Add(-30 * time.Hour * 24).UTC().Unix()
			require.Equal(t, delegationTimeU.Unix(), d.Timestamp)
			require.True(t, d.Amount.IsEqual(expectedDelegation))
			require.Equal(t, creditStart, d.CreditTimestamp)
		}
	}
}

// TestDelegationFirstMonthPairing checks that a delegation is applied in the
// pairing process as soon as it is accepted (regardless of first month)
func TestDelegationFirstMonthPairing(t *testing.T) {
	ts := newTester(t)
	ts.setupForPayments(1, 1, 2)                   // 1 provider, 1 client, 1 providersToPair
	ts.AddAccount(common.CONSUMER, 1, testBalance) // add delegator1

	_, provider := ts.GetAccount(common.PROVIDER, 0)
	_, delegator := ts.GetAccount(common.CONSUMER, 1)

	// increase delegation limit of stake entry
	stakeEntry, found := ts.Keepers.Epochstorage.GetStakeEntryCurrent(ts.Ctx, ts.spec.Index, provider)
	require.True(t, found)
	ts.Keepers.Epochstorage.SetStakeEntryCurrent(ts.Ctx, stakeEntry)
	ts.AdvanceEpoch()

	// delegate and check the delegation's timestamp is equal than nowPlusWeekTime
	delegationTime := ts.BlockTime().UTC().Unix()

	_, err := ts.TxDualstakingDelegate(delegator, provider, sdk.NewCoin(ts.TokenDenom(), sdk.NewInt(testStake)))
	require.NoError(t, err)
	ts.AdvanceEpoch() // apply delegations

	res, err := ts.QueryDualstakingProviderDelegators(provider)
	require.NoError(t, err)
	require.Equal(t, 2, len(res.Delegations)) // expect two because of provider self delegation + delegator
	for _, d := range res.Delegations {
		if d.Delegator == delegator {
			require.Equal(t, delegationTime, d.Timestamp)
		}
	}

	// check that even though a month hasn't passed, the effective stake of the provider is
	// 2*testStake (testStake from provider and testStake from delegator)
	resProviders, err := ts.QueryPairingProviders(ts.spec.Index, false)
	require.NoError(t, err)
	require.Equal(t, 1, len(resProviders.StakeEntry))
	effectiveStake := resProviders.StakeEntry[0].TotalStake()
	require.Equal(t, 2*testStake, effectiveStake.Int64())
}

// TestDelegationFirstMonthReward checks that a delegator is not considered in the reward
// calculation on its first month
func TestDelegationFirstMonthReward(t *testing.T) {
	ts := newTester(t)
	ts.setupForPayments(1, 1, 2)                   // 1 provider, 1 client, 1 providersToPair
	ts.AddAccount(common.CONSUMER, 1, testBalance) // add delegator1

	_, provider := ts.GetAccount(common.PROVIDER, 0)
	_, delegator := ts.GetAccount(common.CONSUMER, 1)

	// increase delegation limit and zero commission of stake entry
	stakeEntry, found := ts.Keepers.Epochstorage.GetStakeEntryCurrent(ts.Ctx, ts.spec.Index, provider)
	require.True(t, found)
	ts.Keepers.Epochstorage.SetStakeEntryCurrent(ts.Ctx, stakeEntry)
	ts.AdvanceEpoch()
	makeProviderCommissionZero(ts, provider)

	// delegate and check the delegation's timestamp is equal to nowPlusWeekTime
	delegationTime := ts.BlockTime().UTC().Unix()

	_, err := ts.TxDualstakingDelegate(delegator, provider, sdk.NewCoin(ts.TokenDenom(), sdk.NewInt(testStake)))
	require.NoError(t, err)

	res, err := ts.QueryDualstakingProviderDelegators(provider)
	require.NoError(t, err)
	require.Equal(t, 2, len(res.Delegations)) // expect two because of provider self delegation + delegator
	for _, d := range res.Delegations {
		if d.Delegator == delegator {
			require.Equal(t, delegationTime, d.Timestamp)
		}
	}

	// to trigger the payment's code, we need to advance a month+blocksToSave. If we do that,
	// the delegation will already mature enough to be part of the reward process. To go around
	// this, we'll call the reward calculation function directly with a fabricated reward just to
	// verify that the delegator gets nothing from the total reward
	fakeReward := sdk.NewCoins(sdk.NewCoin(ts.TokenDenom(), sdk.NewInt(testStake)))
	providerReward, err := ts.Keepers.Dualstaking.RewardProvidersAndDelegators(ts.Ctx, provider, ts.spec.Index,
		fakeReward, subscriptiontypes.ModuleName, true, true, true)
	require.NoError(t, err)
	require.True(t, fakeReward.IsEqual(providerReward), "%v vs %v", providerReward, fakeReward) // if the delegator got something, this would fail

	// verify again that the delegator has no unclaimed rewards
	resRewards, err := ts.QueryDualstakingDelegatorRewards(delegator, provider, ts.spec.Index)
	require.NoError(t, err)
	require.Equal(t, 0, len(resRewards.Rewards))
	// now we advance some time and check that the delegator gets rewards
	ts.AdvanceEpoch() // apply delegations
	providerReward, err = ts.Keepers.Dualstaking.RewardProvidersAndDelegators(ts.Ctx, provider, ts.spec.Index,
		fakeReward, subscriptiontypes.ModuleName, true, true, true)
	require.NoError(t, err)
	require.False(t, fakeReward.IsEqual(providerReward), "%v", providerReward) // if the delegator got anything, this would fail
}

// TestRedelegationFirstMonthReward checks that a delegator that redelegates
// is not considered in the reward calculation on its first month
func TestRedelegationFirstMonthReward(t *testing.T) {
	ts := newTester(t)
	ts.setupForPayments(2, 1, 2)                   // 2 providers, 1 client, 1 providersToPair
	ts.AddAccount(common.CONSUMER, 1, testBalance) // add delegator1

	_, provider := ts.GetAccount(common.PROVIDER, 0)
	_, provider1 := ts.GetAccount(common.PROVIDER, 1)
	_, delegator := ts.GetAccount(common.CONSUMER, 1)

	// increase delegation limit and zero commission of both stake entries
	stakeEntry, found := ts.Keepers.Epochstorage.GetStakeEntryCurrent(ts.Ctx, ts.spec.Index, provider)
	require.True(t, found)
	ts.Keepers.Epochstorage.SetStakeEntryCurrent(ts.Ctx, stakeEntry)
	ts.AdvanceEpoch()
	stakeEntry, found = ts.Keepers.Epochstorage.GetStakeEntryCurrent(ts.Ctx, ts.spec.Index, provider1)
	require.True(t, found)
	ts.Keepers.Epochstorage.SetStakeEntryCurrent(ts.Ctx, stakeEntry)
	ts.AdvanceEpoch()
	makeProviderCommissionZero(ts, provider1)
	makeProviderCommissionZero(ts, provider)

	// delegate and check the delegation's timestamp is equal to nowPlusWeekTime
	delegationTime := ts.BlockTime().UTC().Unix()

	_, err := ts.TxDualstakingDelegate(delegator, provider, sdk.NewCoin(ts.TokenDenom(), sdk.NewInt(testStake)))
	require.NoError(t, err)
	ts.AdvanceEpoch() // apply delegations

	res, err := ts.QueryDualstakingProviderDelegators(provider)
	require.NoError(t, err)
	require.Equal(t, 2, len(res.Delegations)) // expect two because of provider self delegation + delegator
	for _, d := range res.Delegations {
		if d.Delegator == delegator {
			require.Equal(t, delegationTime, d.Timestamp)
		}
	}

	// advance a month a redelegate some of the funds to the second provider
	ts.AdvanceMonths(1)
	redelegateAmount := sdk.NewCoin(ts.TokenDenom(), res.Delegations[0].Amount.Amount.QuoRaw(2))
	_, err = ts.TxDualstakingRedelegate(delegator, provider, provider1, redelegateAmount)
	require.NoError(t, err)

	// to trigger the payment's code, we need to advance a month+blocksToSave. If we do that,
	// the delegation will already mature enough to be part of the reward process. To go around
	// this, we'll call the reward calculation function directly with a fabricated reward just to
	// verify that the delegator gets nothing from the total reward from provider1 but does get
	// reward from provider
	fakeReward := sdk.NewCoins(sdk.NewCoin(ts.TokenDenom(), sdk.NewInt(testStake)))
	provider1Reward, err := ts.Keepers.Dualstaking.RewardProvidersAndDelegators(ts.Ctx, provider1, ts.spec.Index,
		fakeReward, subscriptiontypes.ModuleName, true, false, true)
	require.NoError(t, err)
	require.True(t, fakeReward.IsEqual(provider1Reward)) // if the delegator got anything, this would fail
	providerReward, err := ts.Keepers.Dualstaking.RewardProvidersAndDelegators(ts.Ctx, provider, ts.spec.Index,
		fakeReward, subscriptiontypes.ModuleName, true, false, true)
	require.NoError(t, err)
	require.False(t, fakeReward.IsEqual(providerReward)) // the delegator should have rewards

	// verify again that the delegator has no unclaimed rewards with provider1 but has some with provider
	resRewards, err := ts.QueryDualstakingDelegatorRewards(delegator, provider1, ts.spec.Index)
	require.NoError(t, err)
	require.Equal(t, 0, len(resRewards.Rewards))
	resRewards, err = ts.QueryDualstakingDelegatorRewards(delegator, provider, ts.spec.Index)
	require.NoError(t, err)
	require.Equal(t, 1, len(resRewards.Rewards))
}

// TestDelegatorRewardProviderAddingChain checks that a delegator gets rewards even if the provider moves stake between chains
func TestDelegatorRewardProviderAddingChain(t *testing.T) {
	ts := newTester(t)
	ts.setupForPayments(1, 0, 2)                   // 1 providers, 1 client, 1 providersToPair
	ts.AddAccount(common.CONSUMER, 1, testBalance) // add delegator1

	providerAcc, provider := ts.GetAccount(common.PROVIDER, 0)
	_, delegator := ts.GetAccount(common.CONSUMER, 1)
	clientAcc, client := ts.AddAccount(common.CONSUMER, 0, testBalance)
	makeProviderCommissionZero(ts, provider)

	_, err := ts.TxDualstakingDelegate(delegator, provider, sdk.NewCoin(ts.TokenDenom(), sdk.NewInt(testStake)))
	require.NoError(t, err)
	ts.AdvanceEpoch() // apply delegations

	_, err = ts.TxSubscriptionBuy(client, client, ts.plan.Index, 1, true, false)
	require.NoError(t, err)

	ts.AdvanceEpoch() // to apply pairing

	relayPaymentMessage := sendRelay(ts, provider, clientAcc, []string{ts.spec.Index})
	_, err = ts.TxPairingRelayPayment(relayPaymentMessage.Creator, relayPaymentMessage.Relays...)
	require.Nil(ts.T, err)

	// advance month + blocksToSave + 1 to trigger the provider monthly payment
	ts.AdvanceMonths(1)
	ts.AdvanceEpoch()
	ts.AdvanceBlocks(ts.BlocksToSave() + 1)

	// delegator should get half of the payment
	res, err := ts.QueryDualstakingDelegatorRewards(delegator, provider, "")
	require.Nil(ts.T, err)
	require.Equal(ts.T, ts.plan.Price.Amount.QuoRaw(2), res.Rewards[0].Amount[0].Amount)
	ts.TxDualstakingClaimRewards(delegator, "")

	// add additional spec
	spec1 := common.CreateMockSpec()
	spec1.Index = "mock1"
	spec1.Name = "mock1"
	ts.AddSpec(spec1.Index, spec1)
	err = ts.StakeProvider(providerAcc.GetVaultAddr(), provider, spec1, testStake)
	makeProviderCommissionZero(ts, provider)
	require.NoError(t, err)

	ts.AdvanceEpoch()

	relayPaymentMessage = sendRelay(ts, provider, clientAcc, []string{ts.spec.Index, spec1.Index})
	_, err = ts.TxPairingRelayPayment(relayPaymentMessage.Creator, relayPaymentMessage.Relays...)
	require.Nil(ts.T, err)

	// advance month + blocksToSave + 1 to trigger the provider monthly payment
	ts.AdvanceMonths(1)
	ts.AdvanceEpoch()
	ts.AdvanceBlocks(ts.BlocksToSave() + 1)

	// delegator should get third of the payment
	res, err = ts.QueryDualstakingDelegatorRewards(delegator, provider, "")
	require.Nil(ts.T, err)
	require.Equal(ts.T, ts.plan.Price.Amount.QuoRaw(3).AddRaw(1), res.Rewards[0].Amount[0].Amount)
	ts.TxDualstakingClaimRewards(delegator, "")

	// move the stake between chains, should still get third
	relayPaymentMessage = sendRelay(ts, provider, clientAcc, []string{ts.spec.Index, spec1.Index})
	_, err = ts.TxPairingRelayPayment(relayPaymentMessage.Creator, relayPaymentMessage.Relays...)
	require.Nil(ts.T, err)

	err = ts.Keepers.Pairing.MoveProviderStake(ts.Ctx, provider, ts.spec.Index, spec1.Index, sdk.NewCoin(ts.BondDenom(), sdk.NewInt(testStake/10)))
	require.Nil(ts.T, err)

	// advance month + blocksToSave + 1 to trigger the provider monthly payment
	ts.AdvanceMonths(1)
	ts.AdvanceEpoch()
	ts.AdvanceBlocks(ts.BlocksToSave() + 1)

	// delegator should get half of the payment
	res, err = ts.QueryDualstakingDelegatorRewards(delegator, provider, "")
	require.Nil(ts.T, err)
	require.Equal(ts.T, ts.plan.Price.Amount.QuoRaw(3).AddRaw(1), res.Rewards[0].Amount[0].Amount)
	ts.TxDualstakingClaimRewards(delegator, "")
}
