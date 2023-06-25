package api_tests

import (
	"fmt"
	"math"
	"os"
	"path"
	"strconv"
	"testing"
	"time"

	"github.com/0chain/system_test/internal/api/model"
	"github.com/0chain/system_test/internal/api/util/client"
	"github.com/0chain/system_test/internal/api/util/config"
	"github.com/0chain/system_test/internal/api/util/test"
	"github.com/0chain/system_test/internal/api/util/tokenomics"
	"github.com/0chain/system_test/internal/api/util/wait"
	"github.com/stretchr/testify/require"
)

//nolint:gocyclo
func Test0boxGraphAndTotalEndpoints(testSetup *testing.T) {
	t := test.NewSystemTest(testSetup)

	// Faucet the used wallets
	apiClient.ExecuteFaucet(t, sdkWallet, client.TxSuccessfulStatus) // 18 * 50 * 1e10

	apiClient.ExecuteFaucet(t, blobberOwnerWallet, client.TxSuccessfulStatus)

	ownerBalance := apiClient.GetWalletBalance(t, ownerWallet, client.HttpOkStatus)
	t.Logf("Owner balance: %v", ownerBalance)
	blobberOwnerBalance := apiClient.GetWalletBalance(t, blobberOwnerWallet, client.HttpOkStatus)
	t.Logf("Blobber owner balance: %v", blobberOwnerBalance)
	PrintBalance(t, ownerWallet, blobberOwnerWallet, sdkWallet)
	ownerWallet.Nonce = int(ownerBalance.Nonce)
	blobberOwnerWallet.Nonce = int(blobberOwnerBalance.Nonce)

	// Stake 6 blobbers, each with 1 token
	targetBlobbers, resp, err := apiClient.V1SCRestGetFirstBlobbers(t, 6, client.HttpOkStatus)
	require.NoError(t, err)
	require.Equal(t, 200, resp.StatusCode())
	require.Len(t, targetBlobbers, 6)
	for _, blobber := range targetBlobbers {
		confHash := apiClient.CreateStakePool(t, sdkWallet, 3, blobber.ID, client.TxSuccessfulStatus) // 3zcn from sdkwallet
		require.NotEmpty(t, confHash)
	}

	// Create the free allocation marker (ownerWallet -> sdkWallet)
	apiClient.ExecuteFaucet(t, ownerWallet, client.TxSuccessfulStatus)
	apiClient.AddFreeStorageAssigner(t, ownerWallet, client.TxSuccessfulStatus) // 0.1 ZCN 1 ZCN = 1e10 from owner wallet
	marker := config.CreateFreeStorageMarker(t, sdkWallet.ToSdkWallet(sdkWalletMnemonics), ownerWallet.ToSdkWallet(ownerWalletMnemonics))
	t.Logf("Free allocation marker: %v", marker)

	t.RunSequentiallyWithTimeout("test /v2/graph-write-price", 5*time.Minute, func(t *test.SystemTest) {
		t.RunSequentiallyWithTimeout("endpoint parameters", 5*time.Minute, graphEndpointTestCases(zboxClient.GetGraphWritePrice))
		PrintBalance(t, ownerWallet, blobberOwnerWallet, sdkWallet)

		t.RunSequentiallyWithTimeout("test graph data", 5*time.Minute, func(t *test.SystemTest) {
			PrintBalance(t, ownerWallet, blobberOwnerWallet, sdkWallet)
			data, resp, err := zboxClient.GetGraphWritePrice(t, &model.ZboxGraphRequest{DataPoints: "1"})
			require.NoError(t, err)
			require.Equal(t, 200, resp.StatusCode())
			require.Equal(t, 1, len([]int64(*data)))
			priceBeforeStaking := (*data)[0]

			targetBlobbers, resp, err := apiClient.V1SCRestGetFirstBlobbers(t, 2, client.HttpOkStatus)
			require.NoError(t, err)
			require.Equal(t, 200, resp.StatusCode())
			require.Len(t, targetBlobbers, 2)

			targetBlobbers[0].Capacity += 10 * 1024 * 1024 * 1024
			targetBlobbers[1].Capacity -= 10 * 1024 * 1024 * 1024

			targetBlobbers[0].Terms.WritePrice += *tokenomics.IntToZCN(0.1)
			targetBlobbers[1].Terms.WritePrice += *tokenomics.IntToZCN(0.1)

			apiClient.ExecuteFaucet(t, blobberOwnerWallet, client.TxSuccessfulStatus)
			apiClient.UpdateBlobber(t, blobberOwnerWallet, targetBlobbers[0], client.TxSuccessfulStatus)
			apiClient.UpdateBlobber(t, blobberOwnerWallet, targetBlobbers[1], client.TxSuccessfulStatus)

			wait.PoolImmediately(t, 2*time.Minute, func() bool {
				// get all blobbers
				allBlobbers, resp, err := apiClient.V1SCRestGetAllBlobbers(t, client.HttpOkStatus)
				require.NoError(t, err)
				require.Equal(t, 200, resp.StatusCode())
				//printBlobbers(t, "After Update", allBlobbers)

				expectedAWP := calculateExpectedAvgWritePrice(allBlobbers)
				roundingError := int64(1000)

				data, resp, err := zboxClient.GetGraphWritePrice(t, &model.ZboxGraphRequest{DataPoints: "1"})
				require.NoError(t, err)
				require.Equal(t, 200, resp.StatusCode())
				require.Equal(t, 1, len([]int64(*data)))
				priceAfterStaking := (*data)[0]

				latest, resp, err := zboxClient.GetAverageWritePrice(t)
				require.NoError(t, err)
				require.Equal(t, 200, resp.StatusCode())

				diff := priceAfterStaking - expectedAWP
				t.Logf("priceBeforeStaking: %d, priceAfterStaking: %d, expectedAWP: %d, diff: %d, latest: %d", priceBeforeStaking, priceAfterStaking, expectedAWP, diff, *latest)
				t.Log(priceAfterStaking != priceBeforeStaking && diff >= -roundingError && diff <= roundingError)
				t.Log(priceAfterStaking != priceBeforeStaking && diff >= -roundingError && diff <= roundingError && math.Abs(float64(priceAfterStaking)-float64(*latest)) < 4)

				return priceAfterStaking != priceBeforeStaking && diff >= -roundingError && diff <= roundingError && math.Abs(float64(priceAfterStaking)-float64(*latest)) < 4
			})

			// Cleanup: Revert write price to 0.1
			targetBlobbers[0].Terms.WritePrice = *tokenomics.IntToZCN(0.1)
			targetBlobbers[1].Terms.WritePrice = *tokenomics.IntToZCN(0.1)
			apiClient.UpdateBlobber(t, blobberOwnerWallet, targetBlobbers[0], client.TxSuccessfulStatus)
			apiClient.UpdateBlobber(t, blobberOwnerWallet, targetBlobbers[1], client.TxSuccessfulStatus)
		})
	})

	t.RunSequentiallyWithTimeout("test /v2/graph-total-challenge-pools", 5*time.Minute, func(t *test.SystemTest) {
		t.Run("endpoint parameters", graphEndpointTestCases(zboxClient.GetGraphTotalChallengePools))

		t.Run("test graph data", func(t *test.SystemTest) {
			PrintBalance(t, ownerWallet, blobberOwnerWallet, sdkWallet)
			// Get initial total challenge pools
			data, resp, err := zboxClient.GetGraphTotalChallengePools(t, &model.ZboxGraphRequest{DataPoints: "1"})
			require.NoError(t, err)
			require.Equal(t, 200, resp.StatusCode())
			require.Equal(t, 1, len([]int64(*data)))
			totalChallengePools := (*data)[0]

			// Create a new allocation
			blobberRequirements := model.DefaultBlobberRequirements(sdkWallet.Id, sdkWallet.PublicKey)
			blobberRequirements.DataShards = 1
			blobberRequirements.ParityShards = 1
			allocationBlobbers := apiClient.GetAllocationBlobbers(t, sdkWallet, &blobberRequirements, client.HttpOkStatus)
			allocationID := apiClient.CreateAllocation(t, sdkWallet, allocationBlobbers, client.TxSuccessfulStatus)

			// Upload a file
			sdkClient.UploadFile(t, allocationID)

			var totalChallengePoolsAfterAllocation int64
			wait.PoolImmediately(t, 2*time.Minute, func() bool {
				// Get total challenge pools
				data, resp, err := zboxClient.GetGraphTotalChallengePools(t, &model.ZboxGraphRequest{DataPoints: "1"})
				require.NoError(t, err)
				require.Equal(t, 200, resp.StatusCode())
				require.Equal(t, 1, len([]int64(*data)))
				totalChallengePoolsAfterAllocation = (*data)[0]
				return totalChallengePoolsAfterAllocation > totalChallengePools
			})

			// Cancel the second allocation
			apiClient.CancelAllocation(t, sdkWallet, allocationID, client.TxSuccessfulStatus)

			wait.PoolImmediately(t, 2*time.Minute, func() bool {
				// Get total challenge pools
				data, resp, err := zboxClient.GetGraphTotalChallengePools(t, &model.ZboxGraphRequest{DataPoints: "1"})
				require.NoError(t, err)
				require.Equal(t, 200, resp.StatusCode())
				require.Equal(t, 1, len([]int64(*data)))
				totalChallengePoolsAfterCancel := (*data)[0]
				return totalChallengePoolsAfterCancel < totalChallengePoolsAfterAllocation
			})
		})
	})

	t.RunSequentiallyWithTimeout("test /v2/graph-allocated-storage", 5*time.Minute, func(t *test.SystemTest) {
		t.Run("endpoint parameters", graphEndpointTestCases(zboxClient.GetGraphAllocatedStorage))

		t.Run("test graph data", func(t *test.SystemTest) {
			// Get initial total challenge pools
			PrintBalance(t, ownerWallet, blobberOwnerWallet, sdkWallet)
			data, resp, err := zboxClient.GetGraphAllocatedStorage(t, &model.ZboxGraphRequest{DataPoints: "1"})
			require.NoError(t, err)
			require.Equal(t, 200, resp.StatusCode())
			require.Equal(t, 1, len([]int64(*data)))
			allocatedStorage := (*data)[0]

			// Create a new allocation
			blobberRequirements := model.DefaultBlobberRequirements(sdkWallet.Id, sdkWallet.PublicKey)
			blobberRequirements.DataShards = 1
			blobberRequirements.ParityShards = 1
			allocationBlobbers := apiClient.GetAllocationBlobbers(t, sdkWallet, &blobberRequirements, client.HttpOkStatus)
			allocationID := apiClient.CreateAllocation(t, sdkWallet, allocationBlobbers, client.TxSuccessfulStatus)

			// Check increased
			wait.PoolImmediately(t, 2*time.Minute, func() bool {
				data, resp, err := zboxClient.GetGraphAllocatedStorage(t, &model.ZboxGraphRequest{DataPoints: "1"})
				require.NoError(t, err)
				require.Equal(t, 200, resp.StatusCode())
				require.Equal(t, 1, len([]int64(*data)))
				allocatedStorageAfterAllocation := (*data)[0]
				latest, resp, err := zboxClient.GetTotalAllocatedStorage(t)
				require.NoError(t, err)
				require.Equal(t, 200, resp.StatusCode())
				cond := allocatedStorageAfterAllocation > allocatedStorage && allocatedStorageAfterAllocation == int64(*latest)
				allocatedStorage = allocatedStorageAfterAllocation
				return cond
			})

			// Reduce allocation size
			apiClient.UpdateAllocation(t, sdkWallet, allocationID, &model.UpdateAllocationRequest{
				Size: -1024,
			}, client.TxSuccessfulStatus)

			// Check decreased
			wait.PoolImmediately(t, 2*time.Minute, func() bool {
				data, resp, err := zboxClient.GetGraphAllocatedStorage(t, &model.ZboxGraphRequest{DataPoints: "1"})
				require.NoError(t, err)
				require.Equal(t, 200, resp.StatusCode())
				require.Equal(t, 1, len([]int64(*data)))
				allocatedStorageAfter := (*data)[0]
				cond := allocatedStorageAfter < allocatedStorage
				allocatedStorage = allocatedStorageAfter
				return cond
			})

			// Add blobber to the allocation
			allocation := apiClient.GetAllocation(t, allocationID, client.HttpOkStatus)
			newBlobberID := getNotUsedStorageNodeID(allocationBlobbers.Blobbers, allocation.Blobbers)
			require.NotZero(t, newBlobberID, "New blobber ID contains zero value")
			apiClient.UpdateAllocationBlobbers(t, sdkWallet, newBlobberID, "", allocationID, client.TxSuccessfulStatus)

			// Check increased
			wait.PoolImmediately(t, 2*time.Minute, func() bool {
				data, resp, err := zboxClient.GetGraphAllocatedStorage(t, &model.ZboxGraphRequest{DataPoints: "1"})
				require.NoError(t, err)
				require.Equal(t, 200, resp.StatusCode())
				require.Equal(t, 1, len([]int64(*data)))
				allocatedStorageAfter := (*data)[0]
				latest, resp, err := zboxClient.GetTotalAllocatedStorage(t)
				require.NoError(t, err)
				require.Equal(t, 200, resp.StatusCode())
				cond := allocatedStorageAfter > allocatedStorage && allocatedStorageAfter == int64(*latest)
				allocatedStorage = allocatedStorageAfter
				return cond
			})

			// Cancel allocation
			apiClient.CancelAllocation(t, sdkWallet, allocationID, client.TxSuccessfulStatus)

			// Check decreased + consistency
			wait.PoolImmediately(t, 2*time.Minute, func() bool {
				data, resp, err := zboxClient.GetGraphAllocatedStorage(t, &model.ZboxGraphRequest{DataPoints: "1"})
				require.NoError(t, err)
				require.Equal(t, 200, resp.StatusCode())
				require.Equal(t, 1, len([]int64(*data)))
				allocatedStorageAfter := (*data)[0]
				latest, resp, err := zboxClient.GetTotalAllocatedStorage(t)
				require.NoError(t, err)
				require.Equal(t, 200, resp.StatusCode())
				// FIXME: allocated and saved_data of the blobbers table doesn't decrease when the allocation is canceled. Check https://github.com/0chain/0chain/issues/2211
				cond := (allocatedStorageAfter == allocatedStorage) && (allocatedStorageAfter == int64(*latest)) //nolint
				allocatedStorage = allocatedStorageAfter

				// get all blobbers
				allBlobbers, resp, err := apiClient.V1SCRestGetAllBlobbers(t, client.HttpOkStatus)
				require.NoError(t, err)
				require.Equal(t, 200, resp.StatusCode())
				expectedAllocatedStorage := calculateExpectedAllocated(allBlobbers)
				cond = cond && (allocatedStorageAfter == expectedAllocatedStorage)

				return cond
			})
		})
	})

	t.RunSequentiallyWithTimeout("test /v2/graph-used-storage", 5*time.Minute, func(t *test.SystemTest) {
		t.Run("endpoint parameters", graphEndpointTestCases(zboxClient.GetGraphUsedStorage))

		t.RunWithTimeout("test graph data", 4*time.Minute, func(t *test.SystemTest) {
			PrintBalance(t, ownerWallet, blobberOwnerWallet, sdkWallet)
			// Get initial used storage
			data, resp, err := zboxClient.GetGraphUsedStorage(t, &model.ZboxGraphRequest{DataPoints: "1"})
			require.NoError(t, err)
			require.Equal(t, 200, resp.StatusCode())
			require.Equal(t, 1, len([]int64(*data)))
			usedStorage := (*data)[0]

			// Create a new allocation
			blobberRequirements := model.DefaultBlobberRequirements(sdkWallet.Id, sdkWallet.PublicKey)
			blobberRequirements.DataShards = 1
			blobberRequirements.ParityShards = 1
			allocationBlobbers := apiClient.GetAllocationBlobbers(t, sdkWallet, &blobberRequirements, client.HttpOkStatus)
			allocationID := apiClient.CreateAllocation(t, sdkWallet, allocationBlobbers, client.TxSuccessfulStatus)

			// Upload a file
			fpath, fsize := sdkClient.UploadFile(t, allocationID)

			fmt.Println("Here 0")

			// Check increased
			wait.PoolImmediately(t, 2*time.Minute, func() bool {

				data, resp, err := zboxClient.GetGraphUsedStorage(t, &model.ZboxGraphRequest{DataPoints: "1"})
				require.NoError(t, err)
				require.Equal(t, 200, resp.StatusCode())
				require.Equal(t, 1, len([]int64(*data)))

				fmt.Println("usedStorage : ", usedStorage)
				fmt.Println("usedStorageAfter : ", (*data)[0])
				fmt.Println("fsize : ", fsize)
				fmt.Println("usedStorage - usedStorageAfter : ", usedStorage-(*data)[0])

				usedStorageAfter := (*data)[0]
				cond := (usedStorageAfter - usedStorage) == fsize
				usedStorage = usedStorageAfter
				return cond
			})

			fmt.Println("Here 1")

			// Update with a bigger file
			fpath, newFsize := sdkClient.UpdateFileBigger(t, allocationID, fpath, fsize)
			t.Logf("Filename after update bigger : %v", fpath)

			// Check increased
			wait.PoolImmediately(t, 2*time.Minute, func() bool {
				data, resp, err := zboxClient.GetGraphUsedStorage(t, &model.ZboxGraphRequest{DataPoints: "1"})
				require.NoError(t, err)
				require.Equal(t, 200, resp.StatusCode())
				require.Equal(t, 1, len([]int64(*data)))

				fmt.Println("usedStorage : ", usedStorage)
				fmt.Println("usedStorageAfter : ", (*data)[0])
				fmt.Println("fsize : ", fsize)
				fmt.Println("usedStorage - usedStorageAfter : ", usedStorage-(*data)[0])

				usedStorageAfter := (*data)[0]
				cond := (usedStorageAfter - usedStorage) == (newFsize - fsize)
				usedStorage = usedStorageAfter
				fsize = newFsize
				return cond
			})

			fmt.Println("Here 2")

			// Update with a smaller file
			fpath, newFsize = sdkClient.UpdateFileSmaller(t, allocationID, fpath, newFsize)
			t.Logf("Filename after update smaller : %v", fpath)

			// Check decreased
			wait.PoolImmediately(t, 2*time.Minute, func() bool {
				data, resp, err := zboxClient.GetGraphUsedStorage(t, &model.ZboxGraphRequest{DataPoints: "1"})
				require.NoError(t, err)
				require.Equal(t, 200, resp.StatusCode())
				require.Equal(t, 1, len([]int64(*data)))

				fmt.Println("usedStorage : ", usedStorage)
				fmt.Println("usedStorageAfter : ", (*data)[0])
				fmt.Println("fsize : ", fsize)
				fmt.Println("usedStorage - usedStorageAfter : ", usedStorage-(*data)[0])

				usedStorageAfter := (*data)[0]
				cond := (usedStorage - usedStorageAfter) == (fsize - newFsize)
				usedStorage = usedStorageAfter
				fsize = newFsize
				return cond
			})

			fmt.Println("Here 3")

			// Remove a file
			sdkClient.DeleteFile(t, allocationID, fpath)

			// Check decreased
			wait.PoolImmediately(t, 2*time.Minute, func() bool {
				time.Sleep(5 * time.Second)

				data, resp, err := zboxClient.GetGraphUsedStorage(t, &model.ZboxGraphRequest{DataPoints: "1"})

				require.NoError(t, err)
				require.Equal(t, 200, resp.StatusCode())
				require.Equal(t, 1, len([]int64(*data)))

				fmt.Println("usedStorage : ", usedStorage)
				fmt.Println("usedStorageAfter : ", (*data)[0])
				fmt.Println("fsize : ", fsize)
				fmt.Println("usedStorage - usedStorageAfter : ", usedStorage-(*data)[0])

				usedStorageAfter := (*data)[0]
				cond := (usedStorage - usedStorageAfter) == fsize
				if cond {
					usedStorage = usedStorageAfter
				}
				return cond
			})

			fmt.Println("Here 4")

			// Upload another file
			_, fsize = sdkClient.UploadFile(t, allocationID)

			// Check increased
			wait.PoolImmediately(t, 2*time.Minute, func() bool {
				data, resp, err := zboxClient.GetGraphUsedStorage(t, &model.ZboxGraphRequest{DataPoints: "1"})
				require.NoError(t, err)
				require.Equal(t, 200, resp.StatusCode())
				require.Equal(t, 1, len([]int64(*data)))

				fmt.Println("usedStorage : ", usedStorage)
				fmt.Println("usedStorageAfter : ", (*data)[0])
				fmt.Println("fsize : ", fsize)
				fmt.Println("usedStorage - usedStorageAfter : ", usedStorage-(*data)[0])

				usedStorageAfter := (*data)[0]
				cond := (usedStorageAfter - usedStorage) == fsize
				usedStorage = usedStorageAfter
				return cond
			})

			fmt.Println("Here 5")

			// Cancel the allocation
			apiClient.CancelAllocation(t, sdkWallet, allocationID, client.TxSuccessfulStatus)

			// Check decreased + consistency
			wait.PoolImmediately(t, 2*time.Minute, func() bool {
				// Get total challenge pools
				data, resp, err := zboxClient.GetGraphUsedStorage(t, &model.ZboxGraphRequest{DataPoints: "1"})
				require.NoError(t, err)
				require.Equal(t, 200, resp.StatusCode())
				require.Equal(t, 1, len([]int64(*data)))

				fmt.Println("usedStorage : ", usedStorage)
				fmt.Println("usedStorageAfter : ", (*data)[0])
				fmt.Println("fsize : ", fsize)
				fmt.Println("usedStorage - usedStorageAfter : ", usedStorage-(*data)[0])

				usedStorageAfter := (*data)[0]
				// FIXME: allocated and saved_data of the blobbers table doesn't decrease when the allocation is canceled. Check https://github.com/0chain/0chain/issues/2211
				cond := usedStorage == usedStorageAfter
				usedStorage = usedStorageAfter

				// get all blobbers
				allBlobbers, resp, err := apiClient.V1SCRestGetAllBlobbers(t, client.HttpOkStatus)
				require.NoError(t, err)
				require.Equal(t, 200, resp.StatusCode())

				expectedSavedData := calculateExpectedSavedData(allBlobbers)
				cond = cond && usedStorageAfter == expectedSavedData

				return cond
			})

			fmt.Println("Here 6")
		})
	})

	t.RunSequentiallyWithTimeout("test /v2/graph-total-staked", 5*time.Minute, func(t *test.SystemTest) {
		t.Run("endpoint parameters", graphEndpointTestCases(zboxClient.GetGraphTotalStaked))

		t.RunWithTimeout("test graph data", 4*time.Minute, func(t *test.SystemTest) {
			PrintBalance(t, ownerWallet, blobberOwnerWallet, sdkWallet)
			data, resp, err := zboxClient.GetGraphTotalStaked(t, &model.ZboxGraphRequest{DataPoints: "1"})
			require.NoError(t, err)
			require.Equal(t, 200, resp.StatusCode())
			require.Equal(t, 1, len([]int64(*data)))
			totalStaked := (*data)[0]

			// Stake a blobbers
			targetBlobbers, resp, err := apiClient.V1SCRestGetFirstBlobbers(t, 1, client.HttpOkStatus)
			require.NoError(t, err)
			require.Equal(t, 200, resp.StatusCode())
			require.Len(t, targetBlobbers, 1)
			confHash := apiClient.CreateStakePool(t, sdkWallet, 3, targetBlobbers[0].ID, client.TxSuccessfulStatus)
			require.NotEmpty(t, confHash)

			// Check increased
			wait.PoolImmediately(t, 2*time.Minute, func() bool {
				data, resp, err := zboxClient.GetGraphTotalStaked(t, &model.ZboxGraphRequest{DataPoints: "1"})
				require.NoError(t, err)
				require.Equal(t, 200, resp.StatusCode())
				require.Equal(t, 1, len([]int64(*data)))
				totalStakedAfter := (*data)[0]
				latest, resp, err := zboxClient.GetTotalStaked(t)
				require.NoError(t, err)
				require.Equal(t, 200, resp.StatusCode())
				cond := (totalStakedAfter-totalStaked) == *(tokenomics.IntToZCN(1)) && totalStakedAfter == int64(*latest)
				totalStaked = totalStakedAfter
				return cond
			})

			// Get blobber balance before unlocking
			blobberBalanceBefore := getClientStakeForSSCProvider(t, sdkWallet, targetBlobbers[0].ID)

			// Unlock a stake pool => should decrease
			restake := unstakeBlobber(t, sdkWallet, targetBlobbers[0].ID)
			defer restake()

			// Check decreased
			wait.PoolImmediately(t, 2*time.Minute, func() bool {
				data, resp, err := zboxClient.GetGraphTotalStaked(t, &model.ZboxGraphRequest{DataPoints: "1"})
				require.NoError(t, err)
				require.Equal(t, 200, resp.StatusCode())
				require.Equal(t, 1, len([]int64(*data)))
				totalStakedAfter := (*data)[0]
				latest, resp, err := zboxClient.GetTotalStaked(t)
				require.NoError(t, err)
				require.Equal(t, 200, resp.StatusCode())
				cond := (totalStaked-totalStakedAfter) == blobberBalanceBefore && totalStakedAfter == int64(*latest)
				totalStaked = totalStakedAfter
				return cond
			})

			// Stake a validator
			vs, resp, err := apiClient.V1SCRestGetAllValidators(t, client.HttpOkStatus)
			require.NoError(t, err)
			require.Equal(t, 200, resp.StatusCode())
			require.NotEmpty(t, vs)
			validatorId := vs[0].ValidatorID
			confHash = apiClient.CreateStakePool(t, sdkWallet, 4, validatorId, client.TxSuccessfulStatus)
			require.NotEmpty(t, confHash)

			// Check increase
			wait.PoolImmediately(t, 2*time.Minute, func() bool {
				data, resp, err := zboxClient.GetGraphTotalStaked(t, &model.ZboxGraphRequest{DataPoints: "1"})
				require.NoError(t, err)
				require.Equal(t, 200, resp.StatusCode())
				require.Equal(t, 1, len([]int64(*data)))
				totalStakedAfter := (*data)[0]
				latest, resp, err := zboxClient.GetTotalStaked(t)
				require.NoError(t, err)
				require.Equal(t, 200, resp.StatusCode())
				cond := totalStakedAfter-totalStaked == *tokenomics.IntToZCN(1.0) && totalStakedAfter == int64(*latest)
				totalStaked = totalStakedAfter
				return cond
			})

			// Unstake the validator
			confHash = apiClient.UnlockStakePool(t, sdkWallet, 4, validatorId, client.TxSuccessfulStatus)
			require.NotEmpty(t, confHash)

			// Check decrease
			wait.PoolImmediately(t, 2*time.Minute, func() bool {
				data, resp, err := zboxClient.GetGraphTotalStaked(t, &model.ZboxGraphRequest{DataPoints: "1"})
				require.NoError(t, err)
				require.Equal(t, 200, resp.StatusCode())
				require.Equal(t, 1, len([]int64(*data)))
				totalStakedAfter := (*data)[0]
				latest, resp, err := zboxClient.GetTotalStaked(t)
				require.NoError(t, err)
				require.Equal(t, 200, resp.StatusCode())
				cond := totalStaked-totalStakedAfter == *tokenomics.IntToZCN(1.0) && totalStakedAfter == int64(*latest)
				totalStaked = totalStakedAfter
				return cond
			})

			// Stake a miner
			miners, resp, err := apiClient.V1SCRestGetAllMiners(t, client.HttpOkStatus)
			require.NoError(t, err)
			require.Equal(t, 200, resp.StatusCode())
			require.NotEmpty(t, miners)
			minerId := miners[0].SimpleNodeResponse.ID
			t.Logf("Staking miner %s", minerId)
			confHash = apiClient.CreateMinerStakePool(t, sdkWallet, 1, minerId, float64(1.0), client.TxSuccessfulStatus)
			require.NotEmpty(t, confHash)

			// Check increase
			wait.PoolImmediately(t, 2*time.Minute, func() bool {
				data, resp, err := zboxClient.GetGraphTotalStaked(t, &model.ZboxGraphRequest{DataPoints: "1"})
				require.NoError(t, err)
				require.Equal(t, 200, resp.StatusCode())
				require.Equal(t, 1, len([]int64(*data)))
				totalStakedAfter := (*data)[0]
				latest, resp, err := zboxClient.GetTotalStaked(t)
				require.NoError(t, err)
				require.Equal(t, 200, resp.StatusCode())
				cond := totalStakedAfter-totalStaked == *tokenomics.IntToZCN(1.0) && totalStakedAfter == int64(*latest)
				totalStaked = totalStakedAfter
				return cond
			})

			// Unstake the miner
			confHash = apiClient.UnlockMinerStakePool(t, sdkWallet, 1, minerId, client.TxSuccessfulStatus)
			require.NotEmpty(t, confHash)

			// Check decrease
			wait.PoolImmediately(t, 2*time.Minute, func() bool {
				data, resp, err := zboxClient.GetGraphTotalStaked(t, &model.ZboxGraphRequest{DataPoints: "1"})
				require.NoError(t, err)
				require.Equal(t, 200, resp.StatusCode())
				require.Equal(t, 1, len([]int64(*data)))
				totalStakedAfter := (*data)[0]
				latest, resp, err := zboxClient.GetTotalStaked(t)
				require.NoError(t, err)
				require.Equal(t, 200, resp.StatusCode())
				cond := totalStaked-totalStakedAfter == *tokenomics.IntToZCN(1.0) && totalStakedAfter == int64(*latest)
				totalStaked = totalStakedAfter
				return cond
			})

			// Stake a sharder
			sharders, resp, err := apiClient.V1SCRestGetAllSharders(t, client.HttpOkStatus)
			require.NoError(t, err)
			require.Equal(t, 200, resp.StatusCode())
			require.NotEmpty(t, sharders)
			sharderId := sharders[0].SimpleNodeResponse.ID
			confHash = apiClient.CreateMinerStakePool(t, sdkWallet, 2, sharderId, float64(1.0), client.TxSuccessfulStatus)
			require.NotEmpty(t, confHash)

			// Check increase
			wait.PoolImmediately(t, 2*time.Minute, func() bool {
				data, resp, err := zboxClient.GetGraphTotalStaked(t, &model.ZboxGraphRequest{DataPoints: "1"})
				require.NoError(t, err)
				require.Equal(t, 200, resp.StatusCode())
				require.Equal(t, 1, len([]int64(*data)))
				totalStakedAfter := (*data)[0]
				latest, resp, err := zboxClient.GetTotalStaked(t)
				require.NoError(t, err)
				require.Equal(t, 200, resp.StatusCode())
				cond := totalStakedAfter-totalStaked == *tokenomics.IntToZCN(1.0) && totalStakedAfter == int64(*latest)
				totalStaked = totalStakedAfter
				return cond
			})

			// Unstake the sharder
			confHash = apiClient.UnlockMinerStakePool(t, sdkWallet, 2, sharderId, client.TxSuccessfulStatus)
			require.NotEmpty(t, confHash)

			// Check decrease
			wait.PoolImmediately(t, 2*time.Minute, func() bool {
				data, resp, err := zboxClient.GetGraphTotalStaked(t, &model.ZboxGraphRequest{DataPoints: "1"})
				require.NoError(t, err)
				require.Equal(t, 200, resp.StatusCode())
				require.Equal(t, 1, len([]int64(*data)))
				totalStakedAfter := (*data)[0]
				latest, resp, err := zboxClient.GetTotalStaked(t)
				require.NoError(t, err)
				require.Equal(t, 200, resp.StatusCode())
				cond := totalStaked-totalStakedAfter == *tokenomics.IntToZCN(1.0) && totalStakedAfter == int64(*latest)
				totalStaked = totalStakedAfter
				return cond
			})
		})
	})

	t.RunSequentiallyWithTimeout("test /v2/graph-total-minted", 5*time.Minute, func(t *test.SystemTest) {
		t.Run("endpoint parameters", graphEndpointTestCases(zboxClient.GetGraphTotalMinted))

		t.RunWithTimeout("test graph data", 4*time.Minute, func(t *test.SystemTest) {
			data, resp, err := zboxClient.GetGraphTotalMinted(t, &model.ZboxGraphRequest{DataPoints: "1"})
			require.NoError(t, err)
			require.Equal(t, 200, resp.StatusCode())
			require.Equal(t, 1, len([]int64(*data)))
			totalMinted := (*data)[0]

			// Create a new allocation
			blobberRequirements := model.DefaultBlobberRequirements(sdkWallet.Id, sdkWallet.PublicKey)
			blobberRequirements.DataShards = 1
			blobberRequirements.ParityShards = 1
			allocationBlobbers := apiClient.GetAllocationBlobbers(t, sdkWallet, &blobberRequirements, client.HttpOkStatus)
			allocationID := apiClient.CreateAllocation(t, sdkWallet, allocationBlobbers, client.TxSuccessfulStatus)

			// Upload a file
			sdkClient.UploadFile(t, allocationID)

			// Add/Remove blobber to the allocation
			allocation := apiClient.GetAllocation(t, allocationID, client.HttpOkStatus)
			newBlobberID := getNotUsedStorageNodeID(allocationBlobbers.Blobbers, allocation.Blobbers)
			require.NotZero(t, newBlobberID, "New blobber ID contains zero value")
			apiClient.UpdateAllocationBlobbers(t, sdkWallet, newBlobberID,
				allocation.Blobbers[0].ID, allocationID, client.TxSuccessfulStatus)

			// Unlock the stake pool of the removed blobber
			restake1 := unstakeBlobber(t, sdkWallet, allocation.Blobbers[0].ID)
			defer restake1()

			// Check increased
			wait.PoolImmediately(t, 2*time.Minute, func() bool {
				data, resp, err := zboxClient.GetGraphTotalMinted(t, &model.ZboxGraphRequest{DataPoints: "1"})
				require.NoError(t, err)
				require.Equal(t, 200, resp.StatusCode())
				require.Equal(t, 1, len([]int64(*data)))
				totalMintedAfter := (*data)[0]
				latest, resp, err := zboxClient.GetTotalMinted(t)
				require.NoError(t, err)
				require.Equal(t, 200, resp.StatusCode())
				cond := totalMintedAfter == int64(*latest) && totalMintedAfter > totalMinted
				totalMinted = totalMintedAfter
				return cond
			})

			// Cancel the allocation
			apiClient.CancelAllocation(t, sdkWallet, allocationID, client.TxSuccessfulStatus)

			// Unlock the stake pool of the other blobbers
			restake2 := unstakeBlobber(t, sdkWallet, allocation.Blobbers[1].ID)
			restake3 := unstakeBlobber(t, sdkWallet, newBlobberID)
			defer restake2()
			defer restake3()

			// Check increase
			wait.PoolImmediately(t, 2*time.Minute, func() bool {
				data, resp, err := zboxClient.GetGraphTotalMinted(t, &model.ZboxGraphRequest{DataPoints: "1"})
				require.NoError(t, err)
				require.Equal(t, 200, resp.StatusCode())
				require.Equal(t, 1, len([]int64(*data)))
				totalMintedAfter := (*data)[0]
				cond := totalMintedAfter > totalMinted
				totalMinted = totalMintedAfter
				return cond
			})

			// Create free allocation marker
			apiClient.AddFreeStorageAssigner(t, ownerWallet, client.TxSuccessfulStatus)
			marker := config.CreateFreeStorageMarker(t, sdkWallet.ToSdkWallet(sdkWalletMnemonics), ownerWallet.ToSdkWallet(ownerWalletMnemonics))

			// Create a new allocation
			freeAllocData := &model.FreeAllocationData{
				RecipientPublicKey: sdkWallet.PublicKey,
				Marker:             marker,
			}
			freeAllocationBlobbers := apiClient.GetFreeAllocationBlobbers(t, sdkWallet, freeAllocData, client.HttpOkStatus)
			freeAllocationBlobbers.FreeAllocationData = *freeAllocData
			apiClient.CreateFreeAllocation(t, sdkWallet, freeAllocationBlobbers, client.TxSuccessfulStatus)

			fmt.Println("Here we are")

			// Check increase
			wait.PoolImmediately(t, 2*time.Minute, func() bool {
				data, resp, err := zboxClient.GetGraphTotalMinted(t, &model.ZboxGraphRequest{DataPoints: "1"})
				require.NoError(t, err)
				require.Equal(t, 200, resp.StatusCode())
				require.Equal(t, 1, len([]int64(*data)))
				totalMintedAfter := (*data)[0]
				latest, resp, err := zboxClient.GetTotalMinted(t)
				require.NoError(t, err)
				require.Equal(t, 200, resp.StatusCode())
				cond := totalMintedAfter == int64(*latest) && totalMintedAfter > totalMinted
				totalMinted = totalMintedAfter
				return cond
			})
		})
	})

	t.RunSequentiallyWithTimeout("test /v2/graph-total-locked", 5*time.Minute, func(t *test.SystemTest) {
		t.Run("endpoint parameters", graphEndpointTestCases(zboxClient.GetGraphTotalLocked))

		t.RunWithTimeout("test graph data", 4*time.Minute, func(t *test.SystemTest) {
			data, resp, err := zboxClient.GetGraphTotalLocked(t, &model.ZboxGraphRequest{DataPoints: "1"})
			require.NoError(t, err)
			require.Equal(t, 200, resp.StatusCode())
			require.Equal(t, 1, len([]int64(*data)))
			graphTotalLocked := (*data)[0]

			// Some more stake. It's gonna be tough
			// for i := 0; i < 10; i++ {
			// 	apiClient.ExecuteFaucet(t, sdkWallet, client.TxSuccessfulStatus)
			// }

			// Stake blobber
			blobbers, resp, err := apiClient.V1SCRestGetFirstBlobbers(t, 1, client.HttpOkStatus)
			require.NoError(t, err)
			require.Equal(t, 200, resp.StatusCode())
			require.Equal(t, 1, len(blobbers))
			blobberId := blobbers[0].ID
			t.Logf("Staking blobber %s", blobberId)
			confHash := apiClient.CreateStakePool(t, sdkWallet, 3, blobberId, client.TxSuccessfulStatus)
			require.NotEmpty(t, confHash)

			// Check increase
			wait.PoolImmediately(t, 2*time.Minute, func() bool {
				data, resp, err := zboxClient.GetGraphTotalLocked(t, &model.ZboxGraphRequest{DataPoints: "1"})
				require.NoError(t, err)
				require.Equal(t, 200, resp.StatusCode())
				require.Equal(t, 1, len([]int64(*data)))
				totalLockedAfter := (*data)[0]
				cond := totalLockedAfter-graphTotalLocked == *tokenomics.IntToZCN(1.0)
				graphTotalLocked = totalLockedAfter
				return cond
			})

			fmt.Println("Here 1 ")

			// Get blobber balance before unlocking
			blobberBalanceBefore := getClientStakeForSSCProvider(t, sdkWallet, blobberId)

			// Unstake the blobber
			restake := unstakeBlobber(t, sdkWallet, blobberId)
			defer restake()

			// Check decrease
			wait.PoolImmediately(t, 2*time.Minute, func() bool {
				data, resp, err := zboxClient.GetGraphTotalLocked(t, &model.ZboxGraphRequest{DataPoints: "1"})
				require.NoError(t, err)
				require.Equal(t, 200, resp.StatusCode())
				require.Equal(t, 1, len([]int64(*data)))
				totalLockedAfter := (*data)[0]
				cond := graphTotalLocked-totalLockedAfter == blobberBalanceBefore
				graphTotalLocked = totalLockedAfter
				return cond
			})

			fmt.Println("Here 2 ")

			// Stake a validator
			vs, resp, err := apiClient.V1SCRestGetAllValidators(t, client.HttpOkStatus)
			require.NoError(t, err)
			require.Equal(t, 200, resp.StatusCode())
			require.NotEmpty(t, vs)
			validatorId := vs[0].ValidatorID
			confHash = apiClient.CreateStakePool(t, sdkWallet, 4, validatorId, client.TxSuccessfulStatus)
			require.NotEmpty(t, confHash)

			// Check increase
			wait.PoolImmediately(t, 2*time.Minute, func() bool {
				data, resp, err := zboxClient.GetGraphTotalLocked(t, &model.ZboxGraphRequest{DataPoints: "1"})
				require.NoError(t, err)
				require.Equal(t, 200, resp.StatusCode())
				require.Equal(t, 1, len([]int64(*data)))
				totalLockedAfter := (*data)[0]
				cond := totalLockedAfter-graphTotalLocked == *tokenomics.IntToZCN(1.0)
				graphTotalLocked = totalLockedAfter
				return cond
			})

			fmt.Println("Here 3 ")

			// Unstake the validator
			confHash = apiClient.UnlockStakePool(t, sdkWallet, 4, validatorId, client.TxSuccessfulStatus)
			require.NotEmpty(t, confHash)

			// Check decrease
			wait.PoolImmediately(t, 2*time.Minute, func() bool {
				data, resp, err := zboxClient.GetGraphTotalLocked(t, &model.ZboxGraphRequest{DataPoints: "1"})
				require.NoError(t, err)
				require.Equal(t, 200, resp.StatusCode())
				require.Equal(t, 1, len([]int64(*data)))
				totalLockedAfter := (*data)[0]
				cond := graphTotalLocked-totalLockedAfter == *tokenomics.IntToZCN(1.0)
				graphTotalLocked = totalLockedAfter
				return cond
			})

			fmt.Println("Here 4 ")

			// Stake a miner
			miners, resp, err := apiClient.V1SCRestGetAllMiners(t, client.HttpOkStatus)
			require.NoError(t, err)
			require.Equal(t, 200, resp.StatusCode())
			require.NotEmpty(t, miners)
			minerId := miners[0].SimpleNodeResponse.ID
			t.Logf("Staking miner %s", minerId)
			confHash = apiClient.CreateMinerStakePool(t, sdkWallet, 1, minerId, float64(1.0), client.TxSuccessfulStatus)
			require.NotEmpty(t, confHash)

			// Check increase
			wait.PoolImmediately(t, 2*time.Minute, func() bool {
				data, resp, err := zboxClient.GetGraphTotalLocked(t, &model.ZboxGraphRequest{DataPoints: "1"})
				require.NoError(t, err)
				require.Equal(t, 200, resp.StatusCode())
				require.Equal(t, 1, len([]int64(*data)))
				totalLockedAfter := (*data)[0]
				cond := totalLockedAfter-graphTotalLocked == *tokenomics.IntToZCN(1.0)
				graphTotalLocked = totalLockedAfter
				return cond
			})

			fmt.Println("Here 5 ")

			// Unstake the miner
			confHash = apiClient.UnlockMinerStakePool(t, sdkWallet, 1, minerId, client.TxSuccessfulStatus)
			require.NotEmpty(t, confHash)

			// Check decrease
			wait.PoolImmediately(t, 2*time.Minute, func() bool {
				data, resp, err := zboxClient.GetGraphTotalLocked(t, &model.ZboxGraphRequest{DataPoints: "1"})
				require.NoError(t, err)
				require.Equal(t, 200, resp.StatusCode())
				require.Equal(t, 1, len([]int64(*data)))
				totalLockedAfter := (*data)[0]
				cond := graphTotalLocked-totalLockedAfter == *tokenomics.IntToZCN(1.0)
				graphTotalLocked = totalLockedAfter
				return cond
			})

			fmt.Println("Here 6 ")

			// Stake a sharder
			sharders, resp, err := apiClient.V1SCRestGetAllSharders(t, client.HttpOkStatus)
			require.NoError(t, err)
			require.Equal(t, 200, resp.StatusCode())
			require.NotEmpty(t, sharders)
			sharderId := sharders[0].SimpleNodeResponse.ID
			confHash = apiClient.CreateMinerStakePool(t, sdkWallet, 2, sharderId, float64(1.0), client.TxSuccessfulStatus)
			require.NotEmpty(t, confHash)

			// Check increase
			wait.PoolImmediately(t, 2*time.Minute, func() bool {
				data, resp, err := zboxClient.GetGraphTotalLocked(t, &model.ZboxGraphRequest{DataPoints: "1"})
				require.NoError(t, err)
				require.Equal(t, 200, resp.StatusCode())
				require.Equal(t, 1, len([]int64(*data)))
				totalLockedAfter := (*data)[0]
				cond := totalLockedAfter-graphTotalLocked == *tokenomics.IntToZCN(1.0)
				graphTotalLocked = totalLockedAfter
				return cond
			})

			fmt.Println("Here 7 ")

			// Unstake the sharder
			confHash = apiClient.UnlockMinerStakePool(t, sdkWallet, 2, sharderId, client.TxSuccessfulStatus)
			require.NotEmpty(t, confHash)

			// Check decrease
			wait.PoolImmediately(t, 2*time.Minute, func() bool {
				data, resp, err := zboxClient.GetGraphTotalLocked(t, &model.ZboxGraphRequest{DataPoints: "1"})
				require.NoError(t, err)
				require.Equal(t, 200, resp.StatusCode())
				require.Equal(t, 1, len([]int64(*data)))
				totalLockedAfter := (*data)[0]
				cond := graphTotalLocked-totalLockedAfter == *tokenomics.IntToZCN(1.0)
				graphTotalLocked = totalLockedAfter
				return cond
			})

			fmt.Println("Here 8 ")

			// Create allocation
			blobberRequirements := model.DefaultBlobberRequirements(sdkWallet.Id, sdkWallet.PublicKey)
			blobberRequirements.DataShards = 1
			blobberRequirements.ParityShards = 1
			allocationBlobbers := apiClient.GetAllocationBlobbers(t, sdkWallet, &blobberRequirements, client.HttpOkStatus)
			allocationID := apiClient.CreateAllocationWithLockValue(t, sdkWallet, allocationBlobbers, 0.2, client.TxSuccessfulStatus)

			// Check increase by locked value
			wait.PoolImmediately(t, 2*time.Minute, func() bool {
				data, resp, err := zboxClient.GetGraphTotalLocked(t, &model.ZboxGraphRequest{DataPoints: "1"})
				require.NoError(t, err)
				require.Equal(t, 200, resp.StatusCode())
				require.Equal(t, 1, len([]int64(*data)))
				totalLockedAfter := (*data)[0]
				cond := totalLockedAfter-graphTotalLocked == *tokenomics.IntToZCN(0.2)
				graphTotalLocked = totalLockedAfter
				return cond
			})

			fmt.Println("Here 9 ")

			// Create write pool for the allocation
			confHash = apiClient.CreateWritePool(t, sdkWallet, allocationID, float64(1.0), client.TxSuccessfulStatus)
			require.NotEmpty(t, confHash)

			// Check increase
			wait.PoolImmediately(t, 2*time.Minute, func() bool {
				data, resp, err := zboxClient.GetGraphTotalLocked(t, &model.ZboxGraphRequest{DataPoints: "1"})
				require.NoError(t, err)
				require.Equal(t, 200, resp.StatusCode())
				require.Equal(t, 1, len([]int64(*data)))
				totalLockedAfter := (*data)[0]
				cond := totalLockedAfter-graphTotalLocked == *tokenomics.IntToZCN(1.0)
				graphTotalLocked = totalLockedAfter
				return cond
			})

			// Cancel the allocation
			confHash = apiClient.CancelAllocation(t, sdkWallet, allocationID, client.TxSuccessfulStatus)
			require.NotEmpty(t, confHash)

			// Check decrease and calculate cancellation charge
			var cancellationCharge int64
			wait.PoolImmediately(t, 2*time.Minute, func() bool {
				data, resp, err := zboxClient.GetGraphTotalLocked(t, &model.ZboxGraphRequest{DataPoints: "1"})
				require.NoError(t, err)
				require.Equal(t, 200, resp.StatusCode())
				require.Equal(t, 1, len([]int64(*data)))
				totalLockedAfter := (*data)[0]
				cond := totalLockedAfter < graphTotalLocked
				cancellationCharge = graphTotalLocked - totalLockedAfter
				graphTotalLocked = totalLockedAfter
				return cond
			})

			// Unlock the write pool
			confHash = apiClient.UnlockWritePool(t, sdkWallet, allocationID, client.TxSuccessfulStatus)
			require.NotEmpty(t, confHash)

			// Check decrease by (initial locked value + write pool value - cancellation charge)
			t.Logf("Cancellation charge: %d", cancellationCharge)
			wait.PoolImmediately(t, 2*time.Minute, func() bool {
				data, resp, err := zboxClient.GetGraphTotalLocked(t, &model.ZboxGraphRequest{DataPoints: "1"})
				require.NoError(t, err)
				require.Equal(t, 200, resp.StatusCode())
				require.Equal(t, 1, len([]int64(*data)))
				totalLockedAfter := (*data)[0]
				cond := graphTotalLocked-totalLockedAfter == (*tokenomics.IntToZCN(1.0) + *tokenomics.IntToZCN(0.2) - cancellationCharge)
				graphTotalLocked = totalLockedAfter
				return cond
			})

			// Create read pool
			confHash = apiClient.CreateReadPool(t, sdkWallet, float64(1.0), client.TxSuccessfulStatus)
			require.NotEmpty(t, confHash)

			// Check increase
			wait.PoolImmediately(t, 2*time.Minute, func() bool {
				data, resp, err := zboxClient.GetGraphTotalLocked(t, &model.ZboxGraphRequest{DataPoints: "1"})
				require.NoError(t, err)
				require.Equal(t, 200, resp.StatusCode())
				require.Equal(t, 1, len([]int64(*data)))
				totalLockedAfter := (*data)[0]
				cond := totalLockedAfter-graphTotalLocked == *tokenomics.IntToZCN(1.0)
				graphTotalLocked = totalLockedAfter
				return cond
			})

			// Unlock the read pool
			confHash = apiClient.UnlockReadPool(t, sdkWallet, client.TxSuccessfulStatus)
			require.NotEmpty(t, confHash)

			// Check decrease
			wait.PoolImmediately(t, 2*time.Minute, func() bool {
				data, resp, err := zboxClient.GetGraphTotalLocked(t, &model.ZboxGraphRequest{DataPoints: "1"})
				require.NoError(t, err)
				require.Equal(t, 200, resp.StatusCode())
				require.Equal(t, 1, len([]int64(*data)))
				totalLockedAfter := (*data)[0]
				cond := graphTotalLocked-totalLockedAfter == *tokenomics.IntToZCN(1.0)
				graphTotalLocked = totalLockedAfter
				return cond
			})
		})
	})

	t.RunSequentiallyWithTimeout("test /v2/graph-challenges", 5*time.Minute, func(t *test.SystemTest) {
		t.Run("endpoint parameters", func(t *test.SystemTest) {
			// should fail for invalid parameters
			_, resp, _ := zboxClient.GetGraphChallenges(t, &model.ZboxGraphRequest{From: "AX", To: "20", DataPoints: "5"})
			require.Equal(t, 400, resp.StatusCode())
			require.Contains(t, resp.String(), "invalid from param")

			_, resp, _ = zboxClient.GetGraphChallenges(t, &model.ZboxGraphRequest{From: "10", To: "AX", DataPoints: "5"})
			require.Equal(t, 400, resp.StatusCode())
			require.Contains(t, resp.String(), "invalid to param")

			_, resp, _ = zboxClient.GetGraphChallenges(t, &model.ZboxGraphRequest{From: "10", To: "20", DataPoints: "AX"})
			require.Equal(t, 400, resp.StatusCode())
			require.Contains(t, resp.String(), "invalid data-points query param")

			// should pass for valid parameters (end - start = points)
			data, resp, _ := zboxClient.GetGraphChallenges(t, &model.ZboxGraphRequest{From: "1000", To: "1010", DataPoints: "10"})
			require.Equal(t, 200, resp.StatusCode())
			require.Equal(t, 10, len([]int64(data.TotalChallenges)))
			require.Equal(t, 10, len([]int64(data.SuccessfulChallenges)))

			// should fail for invalid parameters (end < start)
			_, resp, _ = zboxClient.GetGraphChallenges(t, &model.ZboxGraphRequest{From: "10000", To: "1000", DataPoints: "10"})
			require.Equal(t, 400, resp.StatusCode())
			require.Contains(t, resp.String(), "to 1000 less than from 10000")

			// should succeed in case of 1 point
			data, resp, err := zboxClient.GetGraphChallenges(t, &model.ZboxGraphRequest{DataPoints: "1"})
			require.NoError(t, err)
			require.Equal(t, 200, resp.StatusCode())
			require.Equal(t, 1, len([]int64(data.TotalChallenges)))
			require.Equal(t, 1, len([]int64(data.SuccessfulChallenges)))

			// should succeed in case of multiple points
			minerStats, _, err := apiClient.V1MinerGetStats(t, 200)
			require.NoError(t, err)
			latestRound := minerStats.LastFinalizedRound
			data, resp, err = zboxClient.GetGraphChallenges(t, &model.ZboxGraphRequest{From: strconv.FormatInt(latestRound-int64(20), 10), To: strconv.FormatInt(latestRound, 10), DataPoints: "10"})
			require.NoError(t, err)
			require.Equal(t, 200, resp.StatusCode())
			require.Equal(t, 10, len([]int64(data.TotalChallenges)))
			require.Equal(t, 10, len([]int64(data.SuccessfulChallenges)))
		})

		t.Run("test graph data", func(t *test.SystemTest) {
			// Get initial graph data
			data, resp, err := zboxClient.GetGraphChallenges(t, &model.ZboxGraphRequest{DataPoints: "1"})
			require.NoError(t, err)
			require.Equal(t, 200, resp.StatusCode())
			require.Equal(t, 1, len([]int64(data.TotalChallenges)))
			require.Equal(t, 1, len([]int64(data.SuccessfulChallenges)))
			totalChallenges, successfulChallenges := data.TotalChallenges[0], data.SuccessfulChallenges[0]

			// Create an allocation
			blobberRequirements := model.DefaultBlobberRequirements(sdkWallet.Id, sdkWallet.PublicKey)
			blobberRequirements.DataShards = 1
			blobberRequirements.ParityShards = 1
			allocationBlobbers := apiClient.GetAllocationBlobbers(t, sdkWallet, &blobberRequirements, client.HttpOkStatus)
			allocationID := apiClient.CreateAllocation(t, sdkWallet, allocationBlobbers, client.TxSuccessfulStatus)

			// Upload a file
			sdkClient.UploadFile(t, allocationID)

			// Check total challenges increase
			wait.PoolImmediately(t, 2*time.Minute, func() bool {
				data, resp, err := zboxClient.GetGraphChallenges(t, &model.ZboxGraphRequest{DataPoints: "1"})
				require.NoError(t, err)
				require.Equal(t, 200, resp.StatusCode())
				require.Equal(t, 1, len([]int64(data.TotalChallenges)))
				totalChallengesAfter := data.TotalChallenges[0]
				successfulChallengesAfter := data.SuccessfulChallenges[0]
				latestTotal, resp, err := zboxClient.GetTotalChallenges(t)
				require.NoError(t, err)
				require.Equal(t, 200, resp.StatusCode())
				latestSuccessful, resp, err := zboxClient.GetSuccessfulChallenges(t)
				require.NoError(t, err)
				require.Equal(t, 200, resp.StatusCode())
				cond := totalChallengesAfter > totalChallenges && int64(*latestTotal) == totalChallengesAfter && int64(*latestSuccessful) == successfulChallengesAfter
				totalChallenges = totalChallengesAfter
				successfulChallenges = data.SuccessfulChallenges[0]
				return cond
			})

			// Add blobber to the allocation
			allocation := apiClient.GetAllocation(t, allocationID, client.HttpOkStatus)
			newBlobberID := getNotUsedStorageNodeID(allocationBlobbers.Blobbers, allocation.Blobbers)
			require.NotZero(t, newBlobberID, "New blobber ID contains zero value")
			apiClient.UpdateAllocationBlobbers(t, sdkWallet, newBlobberID,
				allocation.Blobbers[0].ID, allocationID, client.TxSuccessfulStatus)

			// Check total challenges increase + successful challenges increase because time has passed since the upload
			wait.PoolImmediately(t, 2*time.Minute, func() bool {
				data, resp, err := zboxClient.GetGraphChallenges(t, &model.ZboxGraphRequest{DataPoints: "1"})
				require.NoError(t, err)
				require.Equal(t, 200, resp.StatusCode())
				require.Equal(t, 1, len([]int64(data.TotalChallenges)))
				totalChallengesAfter := data.TotalChallenges[0]
				cond := totalChallengesAfter > totalChallenges
				totalChallenges = totalChallengesAfter
				successfulChallengesAfter := data.SuccessfulChallenges[0]
				cond = cond && successfulChallengesAfter > successfulChallenges
				successfulChallenges = successfulChallengesAfter
				latestTotal, resp, err := zboxClient.GetTotalChallenges(t)
				require.NoError(t, err)
				require.Equal(t, 200, resp.StatusCode())
				latestSuccessful, resp, err := zboxClient.GetSuccessfulChallenges(t)
				require.NoError(t, err)
				require.Equal(t, 200, resp.StatusCode())
				cond = cond && int64(*latestTotal) == totalChallengesAfter && int64(*latestSuccessful) == successfulChallengesAfter
				return cond
			})
		})
	})

	t.RunSequentiallyWithTimeout("test /v2/graph-token-supply", 5*time.Minute, func(t *test.SystemTest) {
		t.Run("endpoint parameters", graphEndpointTestCases(zboxClient.GetGraphTotalLocked))

		t.RunWithTimeout("test graph data", 4*time.Minute, func(t *test.SystemTest) {
			data, resp, err := zboxClient.GetGraphTokenSupply(t, &model.ZboxGraphRequest{DataPoints: "1"})
			require.NoError(t, err)
			require.Equal(t, 200, resp.StatusCode())
			require.Equal(t, 1, len([]int64(*data)))
			totalSupply := (*data)[0]

			// Create a new allocation
			blobberRequirements := model.DefaultBlobberRequirements(sdkWallet.Id, sdkWallet.PublicKey)
			blobberRequirements.DataShards = 1
			blobberRequirements.ParityShards = 1
			allocationBlobbers := apiClient.GetAllocationBlobbers(t, sdkWallet, &blobberRequirements, client.HttpOkStatus)
			allocationID := apiClient.CreateAllocationWithLockValue(t, sdkWallet, allocationBlobbers, 0.2, client.TxSuccessfulStatus)

			// Check decreased
			wait.PoolImmediately(t, 2*time.Minute, func() bool {
				data, resp, err := zboxClient.GetGraphTokenSupply(t, &model.ZboxGraphRequest{DataPoints: "1"})
				require.NoError(t, err)
				require.Equal(t, 200, resp.StatusCode())
				require.Equal(t, 1, len([]int64(*data)))
				totalSupplyAfter := (*data)[0]
				cond := totalSupplyAfter < totalSupply
				totalSupply = totalSupplyAfter
				return cond
			})

			// Create a write pool for the allocation
			confHash := apiClient.CreateWritePool(t, sdkWallet, allocationID, 1.0, client.TxSuccessfulStatus)
			require.NotEmpty(t, confHash)

			// Check decreased
			wait.PoolImmediately(t, 2*time.Minute, func() bool {
				data, resp, err := zboxClient.GetGraphTokenSupply(t, &model.ZboxGraphRequest{DataPoints: "1"})
				require.NoError(t, err)
				require.Equal(t, 200, resp.StatusCode())
				require.Equal(t, 1, len([]int64(*data)))
				totalSupplyAfter := (*data)[0]
				cond := totalSupplyAfter < totalSupply
				totalSupply = totalSupplyAfter
				return cond
			})

			// Upload a file
			sdkClient.UploadFile(t, allocationID)

			// Add/Remove blobber to the allocation
			allocation := apiClient.GetAllocation(t, allocationID, client.HttpOkStatus)
			newBlobberID := getNotUsedStorageNodeID(allocationBlobbers.Blobbers, allocation.Blobbers)
			require.NotZero(t, newBlobberID, "New blobber ID contains zero value")
			apiClient.UpdateAllocationBlobbers(t, sdkWallet, newBlobberID,
				allocation.Blobbers[0].ID, allocationID, client.TxSuccessfulStatus)

			// Unlock the stake pool of the removed blobber
			restake1 := unstakeBlobber(t, sdkWallet, allocation.Blobbers[0].ID)
			defer restake1()

			// Check increased
			wait.PoolImmediately(t, 2*time.Minute, func() bool {
				data, resp, err := zboxClient.GetGraphTokenSupply(t, &model.ZboxGraphRequest{DataPoints: "1"})
				require.NoError(t, err)
				require.Equal(t, 200, resp.StatusCode())
				require.Equal(t, 1, len([]int64(*data)))
				totalSupplyAfter := (*data)[0]
				cond := totalSupplyAfter > totalSupply
				totalSupply = totalSupplyAfter
				return cond
			})

			// Cancel the allocation
			apiClient.CancelAllocation(t, sdkWallet, allocationID, client.TxSuccessfulStatus)

			// Unlock the write pool
			confHash = apiClient.UnlockWritePool(t, sdkWallet, allocationID, client.TxSuccessfulStatus)
			require.NotEmpty(t, confHash)

			// Check increased
			wait.PoolImmediately(t, 2*time.Minute, func() bool {
				data, resp, err := zboxClient.GetGraphTokenSupply(t, &model.ZboxGraphRequest{DataPoints: "1"})
				require.NoError(t, err)
				require.Equal(t, 200, resp.StatusCode())
				require.Equal(t, 1, len([]int64(*data)))
				totalSupplyAfter := (*data)[0]
				cond := totalSupplyAfter > totalSupply
				totalSupply = totalSupplyAfter
				return cond
			})

			// Unlock the stake pool of the other blobbers
			restake2 := unstakeBlobber(t, sdkWallet, allocation.Blobbers[1].ID)
			restake3 := unstakeBlobber(t, sdkWallet, newBlobberID)
			defer restake2()
			defer restake3()

			// Check increase
			wait.PoolImmediately(t, 2*time.Minute, func() bool {
				data, resp, err := zboxClient.GetGraphTokenSupply(t, &model.ZboxGraphRequest{DataPoints: "1"})
				require.NoError(t, err)
				require.Equal(t, 200, resp.StatusCode())
				require.Equal(t, 1, len([]int64(*data)))
				totalSupplyAfter := (*data)[0]
				cond := totalSupplyAfter > totalSupply
				totalSupply = totalSupplyAfter
				return cond
			})

			// Create free allocation marker
			apiClient.AddFreeStorageAssigner(t, ownerWallet, client.TxSuccessfulStatus)
			marker := config.CreateFreeStorageMarker(t, sdkWallet.ToSdkWallet(sdkWalletMnemonics), ownerWallet.ToSdkWallet(ownerWalletMnemonics))

			// Create a new allocation
			freeAllocData := &model.FreeAllocationData{
				RecipientPublicKey: sdkWallet.PublicKey,
				Marker:             marker,
			}
			freeAllocationBlobbers := apiClient.GetFreeAllocationBlobbers(t, sdkWallet, freeAllocData, client.HttpOkStatus)
			freeAllocationBlobbers.FreeAllocationData = *freeAllocData
			apiClient.CreateFreeAllocation(t, sdkWallet, freeAllocationBlobbers, client.TxSuccessfulStatus)

			// Check increase
			wait.PoolImmediately(t, 2*time.Minute, func() bool {
				data, resp, err := zboxClient.GetGraphTokenSupply(t, &model.ZboxGraphRequest{DataPoints: "1"})
				require.NoError(t, err)
				require.Equal(t, 200, resp.StatusCode())
				require.Equal(t, 1, len([]int64(*data)))
				totalSupplyAfter := (*data)[0]
				cond := totalSupplyAfter > totalSupply
				totalSupply = totalSupplyAfter
				return cond
			})

			// Stake a Miner
			miners, resp, err := apiClient.V1SCRestGetAllMiners(t, client.HttpOkStatus)
			require.NoError(t, err)
			require.Equal(t, 200, resp.StatusCode())
			require.NotEmpty(t, miners)
			minerID := miners[0].ID
			confHash = apiClient.CreateMinerStakePool(t, sdkWallet, 1, minerID, float64(1.0), client.TxSuccessfulStatus)
			require.NotEmpty(t, confHash)

			// Check decreased (staked tokens are burnt)
			wait.PoolImmediately(t, 2*time.Minute, func() bool {
				data, resp, err := zboxClient.GetGraphTokenSupply(t, &model.ZboxGraphRequest{DataPoints: "1"})
				require.NoError(t, err)
				require.Equal(t, 200, resp.StatusCode())
				require.Equal(t, 1, len([]int64(*data)))
				totalSupplyAfter := (*data)[0]
				cond := totalSupplyAfter < totalSupply
				totalSupply = totalSupplyAfter
				return cond
			})

			// Unsake the Miner
			confHash = apiClient.UnlockMinerStakePool(t, sdkWallet, 1, minerID, client.TxSuccessfulStatus)
			require.NotEmpty(t, confHash)

			// Check increased (unstaked tokens are minted)
			wait.PoolImmediately(t, 2*time.Minute, func() bool {
				data, resp, err := zboxClient.GetGraphTokenSupply(t, &model.ZboxGraphRequest{DataPoints: "1"})
				require.NoError(t, err)
				require.Equal(t, 200, resp.StatusCode())
				require.Equal(t, 1, len([]int64(*data)))
				totalSupplyAfter := (*data)[0]
				cond := totalSupplyAfter > totalSupply
				totalSupply = totalSupplyAfter
				return cond
			})

			// Stake a Sharder
			sharders, resp, err := apiClient.V1SCRestGetAllSharders(t, client.HttpOkStatus)
			require.NoError(t, err)
			require.Equal(t, 200, resp.StatusCode())
			require.NotEmpty(t, sharders)
			sharderID := sharders[0].ID
			confHash = apiClient.CreateMinerStakePool(t, sdkWallet, 2, sharderID, float64(1.0), client.TxSuccessfulStatus)
			require.NotEmpty(t, confHash)

			// Check decreased (staked tokens are burnt)
			wait.PoolImmediately(t, 2*time.Minute, func() bool {
				data, resp, err := zboxClient.GetGraphTokenSupply(t, &model.ZboxGraphRequest{DataPoints: "1"})
				require.NoError(t, err)
				require.Equal(t, 200, resp.StatusCode())
				require.Equal(t, 1, len([]int64(*data)))
				totalSupplyAfter := (*data)[0]
				cond := totalSupplyAfter < totalSupply
				totalSupply = totalSupplyAfter
				return cond
			})

			// Unsake the Sharder
			confHash = apiClient.UnlockMinerStakePool(t, sdkWallet, 2, sharderID, client.TxSuccessfulStatus)
			require.NotEmpty(t, confHash)

			// Check increased (unstaked tokens are minted)
			wait.PoolImmediately(t, 2*time.Minute, func() bool {
				data, resp, err := zboxClient.GetGraphTokenSupply(t, &model.ZboxGraphRequest{DataPoints: "1"})
				require.NoError(t, err)
				require.Equal(t, 200, resp.StatusCode())
				require.Equal(t, 1, len([]int64(*data)))
				totalSupplyAfter := (*data)[0]
				cond := totalSupplyAfter > totalSupply
				totalSupply = totalSupplyAfter
				return cond
			})

			// Create read pool
			confHash = apiClient.CreateReadPool(t, sdkWallet, float64(1.0), client.TxSuccessfulStatus)
			require.NotEmpty(t, confHash)

			// Check decrease
			wait.PoolImmediately(t, 2*time.Minute, func() bool {
				data, resp, err := zboxClient.GetGraphTokenSupply(t, &model.ZboxGraphRequest{DataPoints: "1"})
				require.NoError(t, err)
				require.Equal(t, 200, resp.StatusCode())
				require.Equal(t, 1, len([]int64(*data)))
				totalSupplyAfter := (*data)[0]
				cond := totalSupplyAfter < totalSupply
				totalSupply = totalSupplyAfter
				return cond
			})

			// Unlock the read pool
			confHash = apiClient.UnlockReadPool(t, sdkWallet, client.TxSuccessfulStatus)
			require.NotEmpty(t, confHash)

			// Check increase
			wait.PoolImmediately(t, 2*time.Minute, func() bool {
				data, resp, err := zboxClient.GetGraphTokenSupply(t, &model.ZboxGraphRequest{DataPoints: "1"})
				require.NoError(t, err)
				require.Equal(t, 200, resp.StatusCode())
				require.Equal(t, 1, len([]int64(*data)))
				totalSupplyAfter := (*data)[0]
				cond := totalSupplyAfter > totalSupply
				totalSupply = totalSupplyAfter
				return cond
			})

			// TODO: Burn is not working, investigate why
			// // Burn ZCN
			// confHash = apiClient.BurnZcn(t, sdkWallet, parsedConfig.EthereumAddress, float64(1.0), client.TxSuccessfulStatus)
			// require.NotEmpty(t, confHash)

			//nolint
			// // Check decrease
			// wait.PoolImmediately(t, 2 * time.Minute, func() bool {
			// 	data, resp, err := zboxClient.GetGraphTokenSupply(t, &model.ZboxGraphRequest{ DataPoints: "1" })
			// 	require.NoError(t, err)
			// 	require.Equal(t, 200, resp.StatusCode())
			// 	require.Equal(t, 1, len([]int64(*data)))
			// 	totalSupplyAfter := (*data)[0]
			// 	cond := totalSupplyAfter < totalSupply
			// 	totalSupply = totalSupplyAfter
			// 	return cond
			// })
		})
	})

	t.RunSequentiallyWithTimeout("test /v2/total-blobber-capacity", 5*time.Minute, func(t *test.SystemTest) {
		// Get initial
		data, resp, err := zboxClient.GetTotalBlobberCapacity(t)
		require.NoError(t, err)
		require.Equal(t, 200, resp.StatusCode())
		totalBlobberCapacity := int64(*data)

		// Faucet the blobber owner wallet
		apiClient.ExecuteFaucet(t, blobberOwnerWallet, client.TxSuccessfulStatus)

		// Increase capacity of 2 blobber
		targetBlobbers, resp, err := apiClient.V1SCRestGetFirstBlobbers(t, 2, client.HttpOkStatus)
		require.NoError(t, err)
		require.Equal(t, 200, resp.StatusCode())
		require.Len(t, targetBlobbers, 2)

		targetBlobbers[0].Capacity += 10 * 1024 * 1024 * 1024
		targetBlobbers[1].Capacity += 5 * 1024 * 1024 * 1024
		apiClient.UpdateBlobber(t, blobberOwnerWallet, targetBlobbers[0], client.TxSuccessfulStatus)
		apiClient.UpdateBlobber(t, blobberOwnerWallet, targetBlobbers[1], client.TxSuccessfulStatus)

		// Check increase
		wait.PoolImmediately(t, 2*time.Minute, func() bool {
			data, resp, err := zboxClient.GetTotalBlobberCapacity(t)
			require.NoError(t, err)
			require.Equal(t, 200, resp.StatusCode())
			totalBlobberCapacityAfter := int64(*data)
			cond := totalBlobberCapacityAfter > totalBlobberCapacity
			totalBlobberCapacity = totalBlobberCapacityAfter
			return cond
		})

		// Decrease them back
		targetBlobbers[0].Capacity -= 10 * 1024 * 1024 * 1024
		targetBlobbers[1].Capacity -= 5 * 1024 * 1024 * 1024
		apiClient.UpdateBlobber(t, blobberOwnerWallet, targetBlobbers[0], client.TxSuccessfulStatus)
		apiClient.UpdateBlobber(t, blobberOwnerWallet, targetBlobbers[1], client.TxSuccessfulStatus)

		// Check decrease
		wait.PoolImmediately(t, 2*time.Minute, func() bool {
			data, resp, err := zboxClient.GetTotalBlobberCapacity(t)
			require.NoError(t, err)
			require.Equal(t, 200, resp.StatusCode())
			totalBlobberCapacityAfter := int64(*data)
			cond := totalBlobberCapacityAfter < totalBlobberCapacity
			totalBlobberCapacity = totalBlobberCapacityAfter

			blobbers, resp, err := apiClient.V1SCRestGetAllBlobbers(t, client.HttpOkStatus)
			require.NoError(t, err)
			require.Equal(t, 200, resp.StatusCode())
			expectedCapacity := calculateCapacity(blobbers)
			require.Equal(t, expectedCapacity, totalBlobberCapacityAfter, "total capacity should be equal to sum of blobbers capacity")
			return cond
		})
	})
}

//nolint:gocyclo
func Test0boxGraphBlobberEndpoints(testSetup *testing.T) {
	t := test.NewSystemTest(testSetup)
	// Faucet the used wallets
	for i := 0; i < 5; i++ {
		apiClient.ExecuteFaucet(t, sdkWallet, client.TxSuccessfulStatus)
	}
	for i := 0; i < 5; i++ {
		apiClient.ExecuteFaucet(t, blobberOwnerWallet, client.TxSuccessfulStatus)
	}
	blobberOwnerBalance := apiClient.GetWalletBalance(t, blobberOwnerWallet, client.HttpOkStatus)
	t.Logf("Blobber owner balance: %v", blobberOwnerBalance)
	blobberOwnerWallet.Nonce = int(blobberOwnerBalance.Nonce)

	// Stake 6 blobbers, each with 1 token
	targetBlobbers, resp, err := apiClient.V1SCRestGetFirstBlobbers(t, 6, client.HttpOkStatus)
	require.NoError(t, err)
	require.Equal(t, 200, resp.StatusCode())
	require.Len(t, targetBlobbers, 6)
	for _, blobber := range targetBlobbers {
		confHash := apiClient.CreateStakePool(t, sdkWallet, 3, blobber.ID, client.TxSuccessfulStatus)
		require.NotEmpty(t, confHash)
	}

	blobberRequirements := model.DefaultBlobberRequirements(sdkWallet.Id, sdkWallet.PublicKey)
	allocationBlobbers := apiClient.GetAllocationBlobbers(t, sdkWallet, &blobberRequirements, client.HttpOkStatus)
	allocationID := apiClient.CreateAllocation(t, sdkWallet, allocationBlobbers, client.TxSuccessfulStatus)

	allocation := apiClient.GetAllocation(t, allocationID, client.HttpOkStatus)

	blobberID := getFirstUsedStorageNodeID(allocationBlobbers.Blobbers, allocation.Blobbers)
	require.NotZero(t, blobberID, "Blobber ID contains zero value")

	apiClient.CreateStakePool(t, sdkWallet, 3, blobberID, client.TxSuccessfulStatus)

	// TODO: replace with native "Upload API" call
	sdkClient.UploadFile(t, allocationID)

	t.Run("test /v2/graph-blobber-challenges-passed and /v2/graph-blobber-challenges-completed", func(t *test.SystemTest) {
		// Get a single blobber to use in graph parameters test
		blobbers, resp, err := apiClient.V1SCRestGetFirstBlobbers(t, 1, client.HttpOkStatus)
		require.NoError(t, err)
		require.Equal(t, 200, resp.StatusCode())
		require.Len(t, blobbers, 1)

		t.Run("endpoint parameters", graphBlobberEndpointTestCases(zboxClient.GetGraphBlobberChallengesPassed, blobbers[0].ID))

		t.Run("endpoint parameters", graphBlobberEndpointTestCases(zboxClient.GetGraphBlobberChallengesCompleted, blobbers[0].ID))

		t.Run("endpoint parameters", graphBlobberEndpointTestCases(zboxClient.GetGraphBlobberChallengesOpen, blobbers[0].ID))

		t.Run("test graph data", func(t *test.SystemTest) {
			// Create allocation
			blobberRequirements := model.DefaultBlobberRequirements(sdkWallet.Id, sdkWallet.PublicKey)
			blobberRequirements.DataShards = 1
			blobberRequirements.ParityShards = 1
			allocationBlobbers := apiClient.GetAllocationBlobbers(t, sdkWallet, &blobberRequirements, client.HttpOkStatus)
			targetBlobber := (*allocationBlobbers.Blobbers)[0]
			allocationID := apiClient.CreateAllocation(t, sdkWallet, allocationBlobbers, client.TxSuccessfulStatus)

			// Get initial value of one of the blobbers
			data, resp, err := zboxClient.GetGraphBlobberChallengesPassed(t, targetBlobber, &model.ZboxGraphRequest{DataPoints: "1"})
			require.NoError(t, err)
			require.Equal(t, 200, resp.StatusCode())
			require.Len(t, *data, 1)
			challnegesPassed := (*data)[0]

			data, resp, err = zboxClient.GetGraphBlobberChallengesCompleted(t, targetBlobber, &model.ZboxGraphRequest{DataPoints: "1"})
			require.NoError(t, err)
			require.Equal(t, 200, resp.StatusCode())
			require.Len(t, *data, 1)
			challnegesCompleted := (*data)[0]

			data, resp, err = zboxClient.GetGraphBlobberChallengesOpen(t, targetBlobber, &model.ZboxGraphRequest{DataPoints: "1"})
			require.NoError(t, err)
			require.Equal(t, 200, resp.StatusCode())
			require.Len(t, *data, 1)
			challnegesOpen := (*data)[0]

			// Upload file
			fpath, fsize := sdkClient.UploadFile(t, allocationID)
			require.NotEmpty(t, fpath)
			require.NotZero(t, fsize)

			// Check increased for the same blobber
			wait.PoolImmediately(t, 2*time.Minute, func() bool {
				data, resp, err := zboxClient.GetGraphBlobberChallengesPassed(t, targetBlobber, &model.ZboxGraphRequest{DataPoints: "1"})
				require.NoError(t, err)
				require.Equal(t, 200, resp.StatusCode())
				require.Len(t, *data, 1)
				challnegesPassedAfter := (*data)[0]
				cond := challnegesPassedAfter > challnegesPassed

				data, resp, err = zboxClient.GetGraphBlobberChallengesCompleted(t, targetBlobber, &model.ZboxGraphRequest{DataPoints: "1"})
				require.NoError(t, err)
				require.Equal(t, 200, resp.StatusCode())
				require.Len(t, *data, 1)
				challnegesCompletedAfter := (*data)[0]
				cond = cond && challnegesCompletedAfter > challnegesCompleted

				data, resp, err = zboxClient.GetGraphBlobberChallengesOpen(t, targetBlobber, &model.ZboxGraphRequest{DataPoints: "1"})
				require.NoError(t, err)
				require.Equal(t, 200, resp.StatusCode())
				require.Len(t, *data, 1)
				challnegesOpenAfter := (*data)[0]
				cond = cond && challnegesOpenAfter < challnegesOpen

				if cond {
					challnegesPassed = challnegesPassedAfter
					challnegesCompleted = challnegesCompletedAfter
					challnegesOpen = challnegesOpenAfter
				}
				return cond
			})
		})
	})

	t.Run("test /v2/graph-blobber-inactive-rounds", func(t *test.SystemTest) {
		// Get a single blobber to use in graph parameters test
		blobbers, resp, err := apiClient.V1SCRestGetFirstBlobbers(t, 1, client.HttpOkStatus)
		require.NoError(t, err)
		require.Equal(t, 200, resp.StatusCode())
		require.Len(t, blobbers, 1)

		t.Run("endpoint parameters", graphBlobberEndpointTestCases(zboxClient.GetGraphBlobberInactiveRounds, blobbers[0].ID))

		// TODO: Complete if needed
		// t.Run("test graph data", func(t *test.SystemTest) {})
	})

	t.Run("test /v2/graph-blobber-write-price", func(t *test.SystemTest) {
		// Get a single blobber to use in graph parameters test
		blobbers, resp, err := apiClient.V1SCRestGetFirstBlobbers(t, 1, client.HttpOkStatus)
		require.NoError(t, err)
		require.Equal(t, 200, resp.StatusCode())
		require.Len(t, blobbers, 1)

		t.Run("endpoint parameters", graphBlobberEndpointTestCases(zboxClient.GetGraphBlobberWritePrice, blobbers[0].ID))

		t.Run("test graph data", func(t *test.SystemTest) {
			// Get a single blobber to use in graph parameters test
			targetBlobber := blobbers[0]

			// Get initial value of one of the blobbers
			data, resp, err := zboxClient.GetGraphBlobberWritePrice(t, targetBlobber.ID, &model.ZboxGraphRequest{DataPoints: "1"})
			require.NoError(t, err)
			require.Equal(t, 200, resp.StatusCode())
			require.Len(t, *data, 1)
			writePrice := (*data)[0]

			// Faucet blobberOwner wallet
			apiClient.ExecuteFaucet(t, blobberOwnerWallet, client.TxSuccessfulStatus)

			// Increase write price
			targetBlobber.Terms.WritePrice += 1000000000
			apiClient.UpdateBlobber(t, blobberOwnerWallet, targetBlobber, client.TxSuccessfulStatus)

			// Check increased for the same blobber
			wait.PoolImmediately(t, 2*time.Minute, func() bool {
				data, resp, err := zboxClient.GetGraphBlobberWritePrice(t, targetBlobber.ID, &model.ZboxGraphRequest{DataPoints: "1"})
				require.NoError(t, err)
				require.Equal(t, 200, resp.StatusCode())
				require.Len(t, *data, 1)
				afterValue := (*data)[0]
				cond := afterValue > writePrice
				if cond {
					writePrice = afterValue
				}
				return cond
			})

			// Decrease write price
			targetBlobber.Terms.WritePrice -= 1000000000
			apiClient.UpdateBlobber(t, blobberOwnerWallet, targetBlobber, client.TxSuccessfulStatus)

			// Check decreased for the same blobber
			wait.PoolImmediately(t, 2*time.Minute, func() bool {
				data, resp, err := zboxClient.GetGraphBlobberWritePrice(t, targetBlobber.ID, &model.ZboxGraphRequest{DataPoints: "1"})
				require.NoError(t, err)
				require.Equal(t, 200, resp.StatusCode())
				require.Len(t, *data, 1)
				afterValue := (*data)[0]
				cond := afterValue < writePrice
				if cond {
					writePrice = afterValue
				}
				return cond
			})
		})
	})

	t.Run("test /v2/graph-blobber-capacity", func(t *test.SystemTest) {
		// Get a single blobber to use in graph parameters test
		blobbers, resp, err := apiClient.V1SCRestGetFirstBlobbers(t, 1, client.HttpOkStatus)
		require.NoError(t, err)
		require.Equal(t, 200, resp.StatusCode())
		require.Len(t, blobbers, 1)

		t.Run("endpoint parameters", graphBlobberEndpointTestCases(zboxClient.GetGraphBlobberCapacity, blobbers[0].ID))

		t.Run("test graph data", func(t *test.SystemTest) {
			// Get a single blobber to use in graph parameters test
			targetBlobber := blobbers[0]

			// Get initial value of one of the blobbers
			data, resp, err := zboxClient.GetGraphBlobberCapacity(t, targetBlobber.ID, &model.ZboxGraphRequest{DataPoints: "1"})
			require.NoError(t, err)
			require.Equal(t, 200, resp.StatusCode())
			require.Len(t, *data, 1)
			capacity := (*data)[0]

			// Faucet blobberOwner wallet
			apiClient.ExecuteFaucet(t, blobberOwnerWallet, client.TxSuccessfulStatus)

			// Increase capacity
			targetBlobber.Capacity += 1000000000
			apiClient.UpdateBlobber(t, blobberOwnerWallet, targetBlobber, client.TxSuccessfulStatus)

			// Check increased for the same blobber
			wait.PoolImmediately(t, 2*time.Minute, func() bool {
				data, resp, err := zboxClient.GetGraphBlobberCapacity(t, targetBlobber.ID, &model.ZboxGraphRequest{DataPoints: "1"})
				require.NoError(t, err)
				require.Equal(t, 200, resp.StatusCode())
				require.Len(t, *data, 1)
				afterValue := (*data)[0]
				cond := afterValue > capacity
				if cond {
					capacity = afterValue
				}
				return cond
			})

			// Decrease capacity
			targetBlobber.Capacity -= 1000000000
			apiClient.UpdateBlobber(t, blobberOwnerWallet, targetBlobber, client.TxSuccessfulStatus)

			// Check decreased for the same blobber
			wait.PoolImmediately(t, 2*time.Minute, func() bool {
				data, resp, err := zboxClient.GetGraphBlobberCapacity(t, targetBlobber.ID, &model.ZboxGraphRequest{DataPoints: "1"})
				require.NoError(t, err)
				require.Equal(t, 200, resp.StatusCode())
				require.Len(t, *data, 1)
				afterValue := (*data)[0]
				cond := afterValue < capacity
				if cond {
					capacity = afterValue
				}
				return cond
			})
		})
	})

	t.Run("test /v2/graph-blobber-allocated", func(t *test.SystemTest) {
		// Get a single blobber to use in graph parameters test
		blobbers, resp, err := apiClient.V1SCRestGetFirstBlobbers(t, 1, client.HttpOkStatus)
		require.NoError(t, err)
		require.Equal(t, 200, resp.StatusCode())
		require.Len(t, blobbers, 1)

		t.Run("endpoint parameters", graphBlobberEndpointTestCases(zboxClient.GetGraphBlobberAllocated, blobbers[0].ID))

		t.Run("test graph data", func(t *test.SystemTest) {
			// Get allocated of all blobbers
			blobberAllocated := make(map[string]int64)

			blobbers, resp, err := apiClient.V1SCRestGetAllBlobbers(t, client.HttpOkStatus)
			require.NoError(t, err)
			require.Equal(t, 200, resp.StatusCode())
			for _, blobber := range blobbers {
				blobberAllocated[blobber.ID] = blobber.Allocated
			}

			// Create allocation
			blobberRequirements := model.DefaultBlobberRequirements(sdkWallet.Id, sdkWallet.PublicKey)
			blobberRequirements.DataShards = 1
			blobberRequirements.ParityShards = 1
			allocationBlobbers := apiClient.GetAllocationBlobbers(t, sdkWallet, &blobberRequirements, client.HttpOkStatus)
			allocationID := apiClient.CreateAllocationWithLockValue(t, sdkWallet, allocationBlobbers, 0.1, client.TxSuccessfulStatus)

			// Value before allocation
			targetBlobber := (*allocationBlobbers.Blobbers)[0]
			allocated := blobberAllocated[targetBlobber]

			// Check increased for the same blobber
			wait.PoolImmediately(t, 2*time.Minute, func() bool {
				data, resp, err := zboxClient.GetGraphBlobberAllocated(t, targetBlobber, &model.ZboxGraphRequest{DataPoints: "1"})
				require.NoError(t, err)
				require.Equal(t, 200, resp.StatusCode())
				require.Len(t, *data, 1)
				afterValue := (*data)[0]
				cond := afterValue > allocated
				if cond {
					allocated = afterValue
				}
				return cond
			})

			// Cancel the allocation
			confHash := apiClient.CancelAllocation(t, sdkWallet, allocationID, client.TxSuccessfulStatus)
			require.NotEmpty(t, confHash)

			// Check decreased for the same blobber
			wait.PoolImmediately(t, 2*time.Minute, func() bool {
				data, resp, err := zboxClient.GetGraphBlobberAllocated(t, targetBlobber, &model.ZboxGraphRequest{DataPoints: "1"})
				require.NoError(t, err)
				require.Equal(t, 200, resp.StatusCode())
				require.Len(t, *data, 1)
				afterValue := (*data)[0]
				// FIXME: allocated and saved_data of the blobbers table doesn't decrease when the allocation is canceled. Check https://github.com/0chain/0chain/issues/2211
				cond := afterValue == allocated
				if cond {
					allocated = afterValue
				}
				return cond
			})
		})
	})

	t.Run("test /v2/graph-blobber-saved-data", func(t *test.SystemTest) {
		// Get a single blobber to use in graph parameters test
		blobbers, resp, err := apiClient.V1SCRestGetFirstBlobbers(t, 1, client.HttpOkStatus)
		require.NoError(t, err)
		require.Equal(t, 200, resp.StatusCode())
		require.Len(t, blobbers, 1)

		t.Run("endpoint parameters", graphBlobberEndpointTestCases(zboxClient.GetGraphBlobberSavedData, blobbers[0].ID))

		t.Run("test graph data", func(t *test.SystemTest) {
			// Get saved data of all blobbers
			blobberSavedData := make(map[string]int64)

			blobbers, resp, err := apiClient.V1SCRestGetAllBlobbers(t, client.HttpOkStatus)
			require.NoError(t, err)
			require.Equal(t, 200, resp.StatusCode())
			for _, blobber := range blobbers {
				blobberSavedData[blobber.ID] = blobber.SavedData
			}

			// Create allocation
			blobberRequirements := model.DefaultBlobberRequirements(sdkWallet.Id, sdkWallet.PublicKey)
			blobberRequirements.DataShards = 1
			blobberRequirements.ParityShards = 1
			allocationBlobbers := apiClient.GetAllocationBlobbers(t, sdkWallet, &blobberRequirements, client.HttpOkStatus)
			allocationID := apiClient.CreateAllocationWithLockValue(t, sdkWallet, allocationBlobbers, 0.1, client.TxSuccessfulStatus)

			// Value before allocation
			targetBlobber := (*allocationBlobbers.Blobbers)[0]
			savedData := blobberSavedData[targetBlobber]

			// Upload a file
			fpath, fsize := sdkClient.UploadFile(t, allocationID)

			// Check increased for the same blobber
			wait.PoolImmediately(t, 2*time.Minute, func() bool {
				data, resp, err := zboxClient.GetGraphBlobberSavedData(t, targetBlobber, &model.ZboxGraphRequest{DataPoints: "1"})
				require.NoError(t, err)
				require.Equal(t, 200, resp.StatusCode())
				require.Len(t, *data, 1)
				afterValue := (*data)[0]
				cond := afterValue-savedData == fsize
				if cond {
					savedData = afterValue
				}
				return cond
			})

			// Delete the file
			sdkClient.DeleteFile(t, allocationID, fpath)

			// Check decreased for the same blobber
			wait.PoolImmediately(t, 2*time.Minute, func() bool {
				data, resp, err := zboxClient.GetGraphBlobberSavedData(t, targetBlobber, &model.ZboxGraphRequest{DataPoints: "1"})
				require.NoError(t, err)
				require.Equal(t, 200, resp.StatusCode())
				require.Len(t, *data, 1)
				afterValue := (*data)[0]
				cond := savedData-afterValue == fsize
				if cond {
					savedData = afterValue
				}
				return cond
			})

			// Upload another file
			_, fsize = sdkClient.UploadFile(t, allocationID)

			// Check increased for the same blobber
			wait.PoolImmediately(t, 2*time.Minute, func() bool {
				data, resp, err := zboxClient.GetGraphBlobberSavedData(t, targetBlobber, &model.ZboxGraphRequest{DataPoints: "1"})
				require.NoError(t, err)
				require.Equal(t, 200, resp.StatusCode())
				require.Len(t, *data, 1)
				afterValue := (*data)[0]
				cond := afterValue-savedData == fsize
				if cond {
					savedData = afterValue
				}
				return cond
			})

			// Cancel the allocation
			confHash := apiClient.CancelAllocation(t, sdkWallet, allocationID, client.TxSuccessfulStatus)
			require.NotEmpty(t, confHash)

			// Check decreased for the same blobber
			wait.PoolImmediately(t, 2*time.Minute, func() bool {
				data, resp, err := zboxClient.GetGraphBlobberSavedData(t, targetBlobber, &model.ZboxGraphRequest{DataPoints: "1"})
				require.NoError(t, err)
				require.Equal(t, 200, resp.StatusCode())
				require.Len(t, *data, 1)
				afterValue := (*data)[0]

				// FIXME: allocated and saved_data of the blobbers table doesn't decrease when the allocation is canceled. Check
				cond := savedData == afterValue
				if cond {
					savedData = afterValue
				}
				return cond
			})
		})
	})

	t.Run("test /v2/graph-blobber-read-data", func(t *test.SystemTest) {
		// Get a single blobber to use in graph parameters test
		blobbers, resp, err := apiClient.V1SCRestGetFirstBlobbers(t, 1, client.HttpOkStatus)
		require.NoError(t, err)
		require.Equal(t, 200, resp.StatusCode())
		require.Len(t, blobbers, 1)

		t.Run("endpoint parameters", graphBlobberEndpointTestCases(zboxClient.GetGraphBlobberReadData, blobbers[0].ID))

		t.Run("test graph data", func(t *test.SystemTest) {
			// Get read data of all blobbers
			blobberReadData := make(map[string]int64)

			blobbers, resp, err := apiClient.V1SCRestGetAllBlobbers(t, client.HttpOkStatus)
			require.NoError(t, err)
			require.Equal(t, 200, resp.StatusCode())
			for _, blobber := range blobbers {
				blobberReadData[blobber.ID] = blobber.ReadData
			}

			// Create allocation
			blobberRequirements := model.DefaultBlobberRequirements(sdkWallet.Id, sdkWallet.PublicKey)
			blobberRequirements.DataShards = 1
			blobberRequirements.ParityShards = 1
			allocationBlobbers := apiClient.GetAllocationBlobbers(t, sdkWallet, &blobberRequirements, client.HttpOkStatus)
			allocationID := apiClient.CreateAllocationWithLockValue(t, sdkWallet, allocationBlobbers, 0.1, client.TxSuccessfulStatus)

			// Value before allocation
			targetBlobber := (*allocationBlobbers.Blobbers)[0]
			readData := blobberReadData[targetBlobber]

			// Upload a file
			fpath, fsize := sdkClient.UploadFile(t, allocationID)

			// Download the file
			sdkClient.DownloadFile(t, allocationID, fpath, ".")
			defer os.Remove(path.Join(".", fpath))

			// // Check increased for the same blobber
			wait.PoolImmediately(t, 2*time.Minute, func() bool {
				data, resp, err := zboxClient.GetGraphBlobberReadData(t, targetBlobber, &model.ZboxGraphRequest{DataPoints: "1"})
				require.NoError(t, err)
				require.Equal(t, 200, resp.StatusCode())
				require.Len(t, *data, 1)
				afterValue := (*data)[0]
				cond := afterValue-readData == fsize
				if cond {
					readData = afterValue
				}
				return cond
			})
		})
	})

	t.Run("test /v2/graph-blobber-offers-total", func(t *test.SystemTest) {
		// Get a single blobber to use in graph parameters test
		blobbers, resp, err := apiClient.V1SCRestGetFirstBlobbers(t, 1, client.HttpOkStatus)
		require.NoError(t, err)
		require.Equal(t, 200, resp.StatusCode())
		require.Len(t, blobbers, 1)

		t.Run("endpoint parameters", graphBlobberEndpointTestCases(zboxClient.GetGraphBlobberOffersTotal, blobbers[0].ID))

		t.Run("test graph data", func(t *test.SystemTest) {
			// Get offers of all blobbers
			blobberOffersTotal := make(map[string]int64)

			blobbers, resp, err := apiClient.V1SCRestGetAllBlobbers(t, client.HttpOkStatus)
			require.NoError(t, err)
			require.Equal(t, 200, resp.StatusCode())
			for _, blobber := range blobbers {
				data, resp, err := apiClient.V1SCRestGetStakePoolStat(t, model.SCRestGetStakePoolStatRequest{
					ProviderType: "3",
					ProviderID:   blobber.ID,
				}, client.HttpOkStatus)
				t.Logf("SP for blobber %v: %+v", blobber.ID, data)
				require.NoError(t, err)
				require.Equal(t, 200, resp.StatusCode())
				blobberOffersTotal[blobber.ID] = data.OffersTotal
			}

			// Create allocation
			blobberRequirements := model.DefaultBlobberRequirements(sdkWallet.Id, sdkWallet.PublicKey)
			blobberRequirements.DataShards = 1
			blobberRequirements.ParityShards = 1
			allocationBlobbers := apiClient.GetAllocationBlobbers(t, sdkWallet, &blobberRequirements, client.HttpOkStatus)
			allocationID := apiClient.CreateAllocationWithLockValue(t, sdkWallet, allocationBlobbers, 0.1, client.TxSuccessfulStatus)

			// Value before allocation
			targetBlobber := (*allocationBlobbers.Blobbers)[0]
			offersTotal := blobberOffersTotal[targetBlobber]

			// Check increased for the same blobber
			wait.PoolImmediately(t, 2*time.Minute, func() bool {
				data, resp, err := zboxClient.GetGraphBlobberOffersTotal(t, targetBlobber, &model.ZboxGraphRequest{DataPoints: "1"})
				require.NoError(t, err)
				require.Equal(t, 200, resp.StatusCode())
				require.Len(t, *data, 1)
				afterValue := (*data)[0]
				cond := afterValue > offersTotal
				if cond {
					offersTotal = afterValue
				}
				return cond
			})

			// Cancel the allocation
			confHash := apiClient.CancelAllocation(t, sdkWallet, allocationID, client.TxSuccessfulStatus)
			require.NotEmpty(t, confHash)

			// Check decreased for the same blobber
			wait.PoolImmediately(t, 2*time.Minute, func() bool {
				data, resp, err := zboxClient.GetGraphBlobberOffersTotal(t, targetBlobber, &model.ZboxGraphRequest{DataPoints: "1"})
				require.NoError(t, err)
				require.Equal(t, 200, resp.StatusCode())
				require.Len(t, *data, 1)
				afterValue := (*data)[0]
				cond := afterValue < offersTotal
				if cond {
					offersTotal = afterValue
				}
				return cond
			})
		})
	})

	t.Run("test /v2/graph-blobber-unstake-total and /v2/graph-blobber-stake-total", func(t *test.SystemTest) {
		// Get a single blobber to use in graph parameters test
		blobbers, resp, err := apiClient.V1SCRestGetFirstBlobbers(t, 1, client.HttpOkStatus)
		require.NoError(t, err)
		require.Equal(t, 200, resp.StatusCode())
		require.Len(t, blobbers, 1)

		t.Run("endpoint parameters", graphBlobberEndpointTestCases(zboxClient.GetGraphBlobberTotalStake, blobbers[0].ID))

		t.Run("test graph data", func(t *test.SystemTest) {
			targetBlobber := blobbers[0].ID
			data, resp, err := apiClient.V1SCRestGetStakePoolStat(t, model.SCRestGetStakePoolStatRequest{
				ProviderType: "3",
				ProviderID:   targetBlobber,
			}, client.HttpOkStatus)
			t.Logf("SP for blobber %v: %+v", targetBlobber, data)
			require.NoError(t, err)
			require.Equal(t, 200, resp.StatusCode())
			stakeTotal := data.Balance

			// Stake the blobber
			confHash := apiClient.CreateStakePool(t, sdkWallet, 3, targetBlobber, client.TxSuccessfulStatus)
			require.NotEmpty(t, confHash)

			// Check stake increased for the same blobber
			wait.PoolImmediately(t, 2*time.Minute, func() bool {
				data, resp, err := zboxClient.GetGraphBlobberTotalStake(t, targetBlobber, &model.ZboxGraphRequest{DataPoints: "1"})
				require.NoError(t, err)
				require.Equal(t, 200, resp.StatusCode())
				require.Len(t, *data, 1)
				afterValue := (*data)[0]
				cond := afterValue > stakeTotal
				return cond
			})

			// Unstake the blobber
			confHash = apiClient.UnlockStakePool(t, sdkWallet, 3, targetBlobber, client.TxSuccessfulStatus)
			require.NotEmpty(t, confHash)

			// Check unstake increased and stake decrease for the same blobber
			wait.PoolImmediately(t, 2*time.Minute, func() bool {
				data, resp, err := zboxClient.GetGraphBlobberTotalStake(t, targetBlobber, &model.ZboxGraphRequest{DataPoints: "1"})
				require.NoError(t, err)
				require.Equal(t, 200, resp.StatusCode())
				require.Len(t, *data, 1)
				afterValue := (*data)[0]
				cond := afterValue < stakeTotal

				return cond
			})
		})
	})

	t.Run("test /v2/graph-blobber-total-rewards", func(t *test.SystemTest) {
		// Get a single blobber to use in graph parameters test
		blobbers, resp, err := apiClient.V1SCRestGetFirstBlobbers(t, 1, client.HttpOkStatus)
		require.NoError(t, err)
		require.Equal(t, 200, resp.StatusCode())
		require.Len(t, blobbers, 1)

		t.Run("endpoint parameters", graphBlobberEndpointTestCases(zboxClient.GetGraphBlobberTotalRewards, blobbers[0].ID))

		t.Run("test graph data", func(t *test.SystemTest) {
			// Get read data of all blobbers
			blobberRewards := make(map[string]int64)

			blobbers, resp, err := apiClient.V1SCRestGetAllBlobbers(t, client.HttpOkStatus)
			require.NoError(t, err)
			require.Equal(t, 200, resp.StatusCode())
			for _, blobber := range blobbers {
				sp, resp, err := apiClient.V1SCRestGetStakePoolStat(t, model.SCRestGetStakePoolStatRequest{
					ProviderType: "3",
					ProviderID:   blobber.ID,
				}, client.HttpOkStatus)
				require.NoError(t, err)
				require.Equal(t, 200, resp.StatusCode())
				blobberRewards[blobber.ID] = sp.Rewards
			}

			// Create allocation
			blobberRequirements := model.DefaultBlobberRequirements(sdkWallet.Id, sdkWallet.PublicKey)
			blobberRequirements.DataShards = 1
			blobberRequirements.ParityShards = 1
			allocationBlobbers := apiClient.GetAllocationBlobbers(t, sdkWallet, &blobberRequirements, client.HttpOkStatus)
			allocationID := apiClient.CreateAllocationWithLockValue(t, sdkWallet, allocationBlobbers, 0.1, client.TxSuccessfulStatus)

			// Value before allocation
			targetBlobber := (*allocationBlobbers.Blobbers)[0]
			rewards := blobberRewards[targetBlobber]

			// Upload a file
			sdkClient.UploadFile(t, allocationID)

			// Check increased for the same blobber
			wait.PoolImmediately(t, 2*time.Minute, func() bool {
				data, resp, err := zboxClient.GetGraphBlobberTotalRewards(t, targetBlobber, &model.ZboxGraphRequest{DataPoints: "1"})
				require.NoError(t, err)
				require.Equal(t, 200, resp.StatusCode())
				require.Len(t, *data, 1)
				afterValue := (*data)[0]
				cond := afterValue > rewards
				if cond {
					rewards = afterValue
				}
				return cond
			})
		})
	})
}

func graphEndpointTestCases(endpoint model.ZboxGraphEndpoint) func(*test.SystemTest) {
	return func(t *test.SystemTest) {
		// should fail for invalid parameters
		_, resp, err := endpoint(t, &model.ZboxGraphRequest{From: "AX", To: "20", DataPoints: "5"})
		require.Error(t, err)
		require.Equal(t, 400, resp.StatusCode())
		require.Contains(t, resp.String(), "invalid from param")

		_, resp, err = endpoint(t, &model.ZboxGraphRequest{From: "10", To: "AX", DataPoints: "5"})
		require.Error(t, err)
		require.Equal(t, 400, resp.StatusCode())
		require.Contains(t, resp.String(), "invalid to param")

		_, resp, err = endpoint(t, &model.ZboxGraphRequest{From: "10", To: "20", DataPoints: "AX"})
		require.Error(t, err)
		require.Equal(t, 400, resp.StatusCode())
		require.Contains(t, resp.String(), "invalid data-points query param")

		// should not fail for valid parameters (end - start = points)
		data, resp, err := endpoint(t, &model.ZboxGraphRequest{From: "1000", To: "1010", DataPoints: "10"})
		require.NoError(t, err)
		require.Equal(t, 200, resp.StatusCode())
		require.Equal(t, 10, len([]int64(*data)))

		// should fail for invalid parameters (end < start)
		_, resp, err = endpoint(t, &model.ZboxGraphRequest{From: "10000", To: "1000", DataPoints: "10"})
		require.Error(t, err)
		require.Equal(t, 400, resp.StatusCode())
		require.Contains(t, resp.String(), "to 1000 less than from 10000")

		// should succeed in case of 1 point
		data, resp, err = endpoint(t, &model.ZboxGraphRequest{DataPoints: "1"})
		require.NoError(t, err)
		require.Equal(t, 200, resp.StatusCode())
		require.Equal(t, 1, len([]int64(*data)))

		// should succeed in case of multiple points
		minerStats, _, err := apiClient.V1MinerGetStats(t, 200)
		require.NoError(t, err)
		latestRound := minerStats.LastFinalizedRound
		data, resp, err = endpoint(t, &model.ZboxGraphRequest{From: strconv.FormatInt(latestRound-int64(20), 10), To: strconv.FormatInt(latestRound, 10), DataPoints: "10"})
		require.NoError(t, err)
		require.Equal(t, 200, resp.StatusCode())
		require.Equal(t, 10, len([]int64(*data)))
	}
}

func graphBlobberEndpointTestCases(endpoint model.ZboxGraphBlobberEndpoint, blobberId string) func(*test.SystemTest) {
	return func(t *test.SystemTest) {
		// should fail for invalid parameters
		_, resp, _ := endpoint(t, "", &model.ZboxGraphRequest{From: "AX", To: "20", DataPoints: "5"})
		require.Equal(t, 400, resp.StatusCode())
		require.Contains(t, resp.String(), "provider id not provided")

		_, resp, _ = endpoint(t, blobberId, &model.ZboxGraphRequest{From: "AX", To: "20", DataPoints: "5"})
		require.Equal(t, 400, resp.StatusCode())
		require.Contains(t, resp.String(), "invalid from param")

		_, resp, _ = endpoint(t, blobberId, &model.ZboxGraphRequest{From: "10", To: "AX", DataPoints: "5"})
		require.Equal(t, 400, resp.StatusCode())
		require.Contains(t, resp.String(), "invalid to param")

		_, resp, _ = endpoint(t, blobberId, &model.ZboxGraphRequest{From: "10", To: "20", DataPoints: "AX"})
		require.Equal(t, 400, resp.StatusCode())
		require.Contains(t, resp.String(), "invalid data-points query param")

		// should fail for invalid parameters (end < start)
		_, resp, _ = endpoint(t, blobberId, &model.ZboxGraphRequest{From: "10000", To: "1000", DataPoints: "10"})
		require.Equal(t, 400, resp.StatusCode())
		require.Contains(t, resp.String(), "to 1000 less than from 10000")

		// should succeed in case of 1 point
		data, resp, _ := endpoint(t, blobberId, &model.ZboxGraphRequest{DataPoints: "1"})
		require.Equal(t, 200, resp.StatusCode())
		require.Equal(t, 1, len([]int64(*data)))

		// should succeed in case of multiple points
		minerStats, _, err := apiClient.V1MinerGetStats(t, 200)
		require.NoError(t, err)
		latestRound := minerStats.LastFinalizedRound
		time.Sleep(5 * time.Second)
		data, resp, err = endpoint(t, blobberId, &model.ZboxGraphRequest{From: strconv.FormatInt(latestRound-int64(20), 10), To: strconv.FormatInt(latestRound, 10), DataPoints: "10"})
		require.NoError(t, err)
		require.Equal(t, 200, resp.StatusCode())
		require.Equal(t, 10, len([]int64(*data)))
	}
}

func PrintBalance(t *test.SystemTest, ownerWallet, blobberOwnerWallet, sdkWallet *model.Wallet) {
	ownerBalance := apiClient.GetWalletBalance(t, ownerWallet, client.HttpOkStatus)
	t.Logf("Owner balance: %v", ownerBalance)
	blobberOwnerBalance := apiClient.GetWalletBalance(t, blobberOwnerWallet, client.HttpOkStatus)
	t.Logf("Blobber owner balance: %v", blobberOwnerBalance)
	sdkWalletBalance := apiClient.GetWalletBalance(t, sdkWallet, client.HttpOkStatus)
	t.Logf("Blobber owner balance: %v", sdkWalletBalance)
}

func printBlobbers(t *test.SystemTest, tag string, blobbers []*model.SCRestGetBlobberResponse) {
	t.Logf("%v: \n", tag)
	for _, blobber := range blobbers {
		t.Logf("ID: %s", blobber.ID)
		t.Logf("URL: %s", blobber.BaseURL)
		t.Logf("ReadPrice: %+v", blobber.Terms.ReadPrice)
		t.Logf("WritePrice: %+v", blobber.Terms.WritePrice)
		t.Logf("MinLockDemand: %+v", blobber.Terms.MinLockDemand)
		t.Logf("Capacity: %+v", blobber.Capacity)
		t.Logf("Allocated: %+v", blobber.Allocated)
		t.Logf("LastHealthCheck: %+v", blobber.LastHealthCheck)

		t.Logf("TotalStake: %+v", blobber.TotalStake)
		t.Logf("DelegateWallet: %+v", blobber.StakePoolSettings.DelegateWallet)
		t.Logf("MinStake: %+v", blobber.StakePoolSettings.MinStake)
		t.Logf("MaxStake: %+v", blobber.StakePoolSettings.MaxStake)
		t.Logf("NumDelegates: %+v", blobber.StakePoolSettings.NumDelegates)
		t.Logf("ServiceCharge: %+v", blobber.StakePoolSettings.ServiceCharge)
		t.Logf("----------------------------------")
	}
}

func calculateExpectedAvgWritePrice(blobbers []*model.SCRestGetBlobberResponse) (expectedAvgWritePrice int64) {
	var totalWritePrice int64

	totalStakedStorage := int64(0)
	stakedStorage := make([]int64, 0, len(blobbers))
	for _, blobber := range blobbers {
		ss := (float64(blobber.TotalStake) / float64(blobber.Terms.WritePrice)) * model.GB
		stakedStorage = append(stakedStorage, int64(ss))
		totalStakedStorage += int64(ss)
	}

	for i, blobber := range blobbers {
		totalWritePrice += int64((float64(stakedStorage[i]) / float64(totalStakedStorage)) * float64(blobber.Terms.WritePrice))
	}
	return totalWritePrice
}

func calculateExpectedAllocated(blobbers []*model.SCRestGetBlobberResponse) int64 {
	var totalAllocatedData int64

	for _, blobber := range blobbers {
		totalAllocatedData += blobber.Allocated
	}
	return totalAllocatedData
}

func calculateExpectedSavedData(blobbers []*model.SCRestGetBlobberResponse) int64 {
	var totalSavedData int64

	for _, blobber := range blobbers {
		totalSavedData += blobber.SavedData
	}
	return totalSavedData
}

func calculateCapacity(blobbers []*model.SCRestGetBlobberResponse) int64 {
	var totalCapacity int64

	for _, blobber := range blobbers {
		totalCapacity += blobber.Capacity
	}
	return totalCapacity
}

func unstakeBlobber(t *test.SystemTest, wallet *model.Wallet, blobberId string) func() {
	confHash := apiClient.UnlockStakePool(t, wallet, 3, blobberId, client.TxSuccessfulStatus)
	require.NotEmpty(t, confHash)
	return func() {
		// Re-stake the blobber
		confHash := apiClient.CreateStakePool(t, wallet, 3, blobberId, client.TxSuccessfulStatus)
		require.NotEmpty(t, confHash)
	}
}

// getClientStakeForSSCProvider returns the stake of the client for the given Storage Smart Contract provider (Blobber/Validator)
func getClientStakeForSSCProvider(t *test.SystemTest, wallet *model.Wallet, providerId string) int64 {
	stake, resp, err := apiClient.V1SCRestGetUserStakePoolStat(t, model.SCRestGetUserStakePoolStatRequest{
		ClientId: wallet.Id,
	}, client.HttpOkStatus)
	require.NoError(t, err)
	require.Equal(t, 200, resp.StatusCode())
	require.NotEmpty(t, stake)

	providerStake := (*stake.Pools[providerId])[0].Balance
	return providerStake
}
