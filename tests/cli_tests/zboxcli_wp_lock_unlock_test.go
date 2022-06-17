package cli_tests

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"testing"
	"time"

	climodel "github.com/0chain/system_test/internal/cli/model"
	cliutils "github.com/0chain/system_test/internal/cli/util"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestWritePoolLockUnlock(t *testing.T) {
	t.Parallel()

	t.Run("Creating allocation moves tokens from wallet to write pool", func(t *testing.T) {
		t.Parallel()

		output, err := registerWallet(t, configPath)
		require.Nil(t, err, "registering wallet failed", strings.Join(output, "\n"))

		output, err = executeFaucetWithTokens(t, configPath, 2.0)
		require.Nil(t, err, "faucet execution failed", strings.Join(output, "\n"))

		// Write Pool must not exist before allocation is created
		output, err = writePoolInfo(t, configPath, false)
		require.Len(t, output, 2)
		require.NotNil(t, err)

		// FIXME: CLI shows error requesting "read" pool info when it should show "write"
		require.Equal(t, "Failed to get write pool info: error requesting read pool info:", output[0])
		require.Equal(t, "resource_not_found: can't get write pool: value not present", output[1])

		// Lock 0.5 token for allocation
		allocParams := createParams(map[string]interface{}{
			"expire": "5m",
			"size":   "1024",
			"lock":   "0.5",
		})
		output, err = createNewAllocation(t, configPath, allocParams)
		require.Nil(t, err, "Failed to create new allocation", strings.Join(output, "\n"))

		require.Len(t, output, 1)
		require.Regexp(t, regexp.MustCompile("Allocation created: ([a-f0-9]{64})"), output[0], "Allocation creation output did not match expected")
		allocationID := strings.Fields(output[0])[2]

		// Wallet balance before lock should be 1.5 ZCN
		output, err = getBalance(t, configPath)
		require.Nil(t, err, "Error fetching balance", strings.Join(output, "\n"))
		require.Len(t, output, 1)
		require.Regexp(t, regexp.MustCompile(`Balance: 1.500 ZCN \(\d*\.?\d+ USD\)$`), output[0])

		// Lock 1 token in Write pool amongst all blobbers
		params := createParams(map[string]interface{}{
			"allocation": allocationID,
			"duration":   "2m",
			"tokens":     1,
		})
		output, err = writePoolLock(t, configPath, params, true)
		require.Nil(t, err, "Failed to lock write tokens", strings.Join(output, "\n"))
		require.Len(t, output, 1)
		require.Equal(t, "locked", output[0])

		lockTimer := time.NewTimer(time.Minute * 2)

		// Wallet balance should decrement from 1.5 to 0.5 ZCN
		output, err = getBalance(t, configPath)
		require.Nil(t, err, "Error fetching balance", strings.Join(output, "\n"))
		require.Len(t, output, 1)
		require.Regexp(t, regexp.MustCompile(`Balance: 500.00\d mZCN \(\d*\.?\d+ USD\)$`), output[0])

		// Write pool balance should increment to 1
		output, err = writePoolInfo(t, configPath, true)
		require.Len(t, output, 1, strings.Join(output, "\n"))
		require.Nil(t, err, "error fetching write pool info", strings.Join(output, "\n"))

		writePools := []climodel.WritePoolInfo{}
		err = json.Unmarshal([]byte(output[0]), &writePools)
		require.Nil(t, err, "Error unmarshalling write pool", strings.Join(output, "\n"))
		require.NotEmpty(t, writePools)

		// We will have two write pools, one created autmatically by newallocation command
		// And one created by wp-lock. We are interested in the latter.
		customWritePoolId := ""
		for _, writePool := range writePools {
			t.Logf("The following information is for WritePool Id [%v] and allocation Id [%v]", writePool.Id, allocationID)
			require.Regexp(t, regexp.MustCompile("([a-f0-9]{64})"), writePool.Id)

			// The write pool created automatically has the same ID as allocation ID
			if writePool.Id != allocationID {
				customWritePoolId = writePool.Id
				t.Log("Actual Write Pool Balance: ", intToZCN(writePool.Balance), "Expected Write Pool Balance: ", 1.0)
				assert.Equal(t, 1.0, intToZCN(writePool.Balance), "Write pool balance did not match amount locked")
				require.GreaterOrEqual(t, time.Now().Add(time.Minute*2).Unix(), writePool.ExpireAt,
					"Time.Now().Unix()+120 should have been greater than or equal to ExpireAt value since WritePool was prior to this with 120 second expiry")
			} else {
				t.Log("Actual Write Pool Balance: ", intToZCN(writePool.Balance), "Expected Write Pool Balance: ", 1.0)
				assert.Equal(t, 0.5, intToZCN(writePool.Balance), "Write pool balance did not match amount locked")
				// Weird Behavior Noted: The automatic write pool generated by newallocation command
				// expires 120 seconds after allocation expiration
				require.GreaterOrEqual(t, time.Now().Add(time.Minute*7).Unix(), writePool.ExpireAt,
					"Time.Now().Unix()+300 should have been greater than or equal to ExpireAt value since WritePool was prior to this with 120 second expiry")
			}

			require.Equal(t, allocationID, writePool.AllocationId)
			require.Less(t, 0, len(writePool.Blobber))
			require.Equal(t, true, writePool.Locked)

			/*
			**	Total write pool balance should be divided as per read_price ratio amongst each blobber
			 */

			// Get total
			blobbersInAllocation := map[string]climodel.BlobberDetails{}
			var total float64
			for i := 0; i < len(writePool.Blobber); i++ {
				output, err = getBlobberInfo(t, configPath, createParams(map[string]interface{}{"json": "", "blobber_id": writePool.Blobber[i].BlobberID}))
				require.Nil(t, err, strings.Join(output, "\n"))
				require.Len(t, output, 1)

				var blobberInfo climodel.BlobberDetails
				err = json.Unmarshal([]byte(output[0]), &blobberInfo)
				require.Nil(t, err, strings.Join(output, "\n"))

				total += intToZCN(blobberInfo.Terms.Write_price)
				blobbersInAllocation[writePool.Blobber[i].BlobberID] = blobberInfo
			}

			// Blobber write pool balance should be (pool Balance)/blobber.Terms.WritePrice ZCN in each
			for i := 0; i < len(writePool.Blobber); i++ {
				t.Logf("\t\tThe following information is for Blobber Id [%v]", writePool.Blobber[i].BlobberID)
				t.Log("For Blobber ID ", writePool.Blobber[i].BlobberID, ": ")

				ratio := intToZCN(blobbersInAllocation[writePool.Blobber[i].BlobberID].Terms.Write_price) / total

				t.Log("\t\t\tActual Write Pool Blobber Balance: ", intToZCN(writePool.Blobber[i].Balance))
				t.Log("\t\t\tExpected: Write Pool Blobber Balance", intToZCN(writePool.Balance)/float64(len(writePool.Blobber)))
				assert.Equal(t, intToZCN(writePool.Balance)*ratio, intToZCN(writePool.Blobber[i].Balance))
			}
		}

		// Wait until timer expirted
		<-lockTimer.C

		params = createParams(map[string]interface{}{
			"pool_id": customWritePoolId,
		})
		output, err = writePoolUnlock(t, configPath, params, true)
		require.Nil(t, err, "Unable to unlock tokens", strings.Join(output, "\n"))

		require.Len(t, output, 1)
		require.Equal(t, "unlocked", output[0])

		// Wallet balance should increment from 0.5 to 1.5 ZCN
		output, err = getBalance(t, configPath)
		require.Nil(t, err, "Error fetching balance", strings.Join(output, "\n"))
		require.Len(t, output, 1)
		require.Regexp(t, regexp.MustCompile(`Balance: 1.500 ZCN \(\d*\.?\d+ USD\)$`), output[0])
	})

	t.Run("Should not be able to lock more write tokens than wallet balance", func(t *testing.T) {
		t.Parallel()

		output, err := registerWallet(t, configPath)
		require.Nil(t, err, "registering wallet failed", strings.Join(output, "\n"))

		output, err = executeFaucetWithTokens(t, configPath, 1.0)
		require.Nil(t, err, "faucet execution failed", strings.Join(output, "\n"))

		// Lock 0.5 token for allocation
		allocParams := createParams(map[string]interface{}{
			"expire": "5m",
			"size":   "1024",
			"lock":   "0.5",
		})
		output, err = createNewAllocation(t, configPath, allocParams)
		require.Nil(t, err, "Failed to create new allocation", strings.Join(output, "\n"))

		require.Len(t, output, 1)
		require.Regexp(t, regexp.MustCompile("Allocation created: ([a-f0-9]{64})"), output[0], "Allocation creation output did not match expected")
		allocationID := strings.Fields(output[0])[2]

		// Wallet balance before lock should be 0.5 ZCN
		output, err = getBalance(t, configPath)
		require.Nil(t, err, "Error fetching balance", strings.Join(output, "\n"))
		require.Len(t, output, 1)
		require.Regexp(t, regexp.MustCompile(`Balance: 500.00\d mZCN \(\d*\.?\d+ USD\)$`), output[0])

		// Lock 1 token in write pool distributed amongst all blobbers
		params := createParams(map[string]interface{}{
			"allocation": allocationID,
			"tokens":     1,
			"duration":   "5m",
		})
		output, err = writePoolLock(t, configPath, params, false)
		require.NotNil(t, err, "Locked more tokens than in wallet", strings.Join(output, "\n"))
		require.True(t, len(output) > 0, "expected output length be at least 1")
		require.Equal(t, "Failed to lock tokens in write pool: write_pool_lock_failed: lock amount is greater than balance", output[0], strings.Join(output, "\n"))

		// Wallet balance should remain same
		output, err = getBalance(t, configPath)
		require.Nil(t, err, "Error fetching balance", strings.Join(output, "\n"))
		require.Len(t, output, 1)
		require.Regexp(t, regexp.MustCompile(`Balance: 500.00\d mZCN \(\d*\.?\d+ USD\)$`), output[0])
	})

	t.Run("Should not be able to lock negative write tokens", func(t *testing.T) {
		t.Parallel()

		output, err := registerWallet(t, configPath)
		require.Nil(t, err, "registering wallet failed", strings.Join(output, "\n"))

		output, err = executeFaucetWithTokens(t, configPath, 1.0)
		require.Nil(t, err, "faucet execution failed", strings.Join(output, "\n"))

		// Lock 0.5 token for allocation
		allocParams := createParams(map[string]interface{}{
			"expire": "5m",
			"size":   "1024",
			"lock":   "0.5",
		})
		output, err = createNewAllocation(t, configPath, allocParams)
		require.Nil(t, err, "Failed to create new allocation", strings.Join(output, "\n"))

		require.Len(t, output, 1)
		require.Regexp(t, regexp.MustCompile("Allocation created: ([a-f0-9]{64})"), output[0], "Allocation creation output did not match expected")
		allocationID := strings.Fields(output[0])[2]

		// Wallet balance before lock should be 0.5 ZCN
		output, err = getBalance(t, configPath)
		require.Nil(t, err, "Error fetching balance", strings.Join(output, "\n"))
		require.Len(t, output, 1)
		require.Regexp(t, regexp.MustCompile(`Balance: 500.00\d mZCN \(\d*\.?\d+ USD\)$`), output[0])

		// Locking -1 token in write pool should not succeed
		params := createParams(map[string]interface{}{
			"allocation": allocationID,
			"tokens":     -1,
			"duration":   "5m",
		})
		output, err = writePoolLock(t, configPath, params, false)
		require.NotNil(t, err, "Locked negative tokens", strings.Join(output, "\n"))
		require.True(t, len(output) > 0, "expected output length be at least 1")
		require.Equal(t, "invalid token amount: negative", output[0], strings.Join(output, "\n"))

		// Wallet balance should remain same
		output, err = getBalance(t, configPath)
		require.Nil(t, err, "Error fetching balance", strings.Join(output, "\n"))
		require.Len(t, output, 1)
		require.Regexp(t, regexp.MustCompile(`Balance: 500.00\d mZCN \(\d*\.?\d+ USD\)$`), output[0])
	})

	t.Run("Should not be able to lock zero write tokens", func(t *testing.T) {
		t.Parallel()

		output, err := registerWallet(t, configPath)
		require.Nil(t, err, "registering wallet failed", strings.Join(output, "\n"))

		output, err = executeFaucetWithTokens(t, configPath, 1.0)
		require.Nil(t, err, "faucet execution failed", strings.Join(output, "\n"))

		// Lock 0.5 token for allocation
		allocParams := createParams(map[string]interface{}{
			"expire": "5m",
			"size":   "1024",
			"lock":   "0.5",
		})
		output, err = createNewAllocation(t, configPath, allocParams)
		require.Nil(t, err, "Failed to create new allocation", strings.Join(output, "\n"))

		require.Len(t, output, 1)
		require.Regexp(t, regexp.MustCompile("Allocation created: ([a-f0-9]{64})"), output[0], "Allocation creation output did not match expected")
		allocationID := strings.Fields(output[0])[2]

		// Wallet balance before lock should be 0.5 ZCN
		output, err = getBalance(t, configPath)
		require.Nil(t, err, "Error fetching balance", strings.Join(output, "\n"))
		require.Len(t, output, 1)
		require.Regexp(t, regexp.MustCompile(`Balance: 500.00\d mZCN \(\d*\.?\d+ USD\)$`), output[0])

		// Locking 0 token in write pool should not succeed
		params := createParams(map[string]interface{}{
			"allocation": allocationID,
			"tokens":     0,
			"duration":   "5m",
		})
		output, err = writePoolLock(t, configPath, params, false)
		require.NotNil(t, err, "Locked 0 tokens", strings.Join(output, "\n"))
		require.True(t, len(output) > 0, "expected output length be at least 1")
		require.Equal(t, "Failed to lock tokens in write pool: write_pool_lock_failed: insufficient amount to lock", output[0], strings.Join(output, "\n"))

		// Wallet balance should remain same
		output, err = getBalance(t, configPath)
		require.Nil(t, err, "Error fetching balance", strings.Join(output, "\n"))
		require.Len(t, output, 1)
		require.Regexp(t, regexp.MustCompile(`Balance: 500.00\d mZCN \(\d*\.?\d+ USD\)$`), output[0])
	})

	t.Run("Missing tokens flag should result in error", func(t *testing.T) {
		t.Parallel()

		output, err := registerWallet(t, configPath)
		require.Nil(t, err, "registering wallet failed", strings.Join(output, "\n"))

		output, err = executeFaucetWithTokens(t, configPath, 1.0)
		require.Nil(t, err, "faucet execution failed", strings.Join(output, "\n"))

		// Lock 0.5 token for allocation
		allocParams := createParams(map[string]interface{}{
			"expire": "5m",
			"size":   "1024",
			"lock":   "0.5",
		})
		output, err = createNewAllocation(t, configPath, allocParams)
		require.Nil(t, err, "Failed to create new allocation", strings.Join(output, "\n"))

		require.Len(t, output, 1)
		require.Regexp(t, regexp.MustCompile("Allocation created: ([a-f0-9]{64})"), output[0], "Allocation creation output did not match expected")
		allocationID := strings.Fields(output[0])[2]

		// Not specifying amount to lock should not succeed
		params := createParams(map[string]interface{}{
			"allocation": allocationID,
			"duration":   "5m",
		})
		output, err = writePoolLock(t, configPath, params, false)
		require.NotNil(t, err, "Locked tokens without providing amount to lock", strings.Join(output, "\n"))
		require.Len(t, output, 1)
		require.Equal(t, "missing required 'tokens' flag", output[0])
	})

	t.Run("Missing duration flag should result in error", func(t *testing.T) {
		t.Parallel()

		output, err := registerWallet(t, configPath)
		require.Nil(t, err, "registering wallet failed", strings.Join(output, "\n"))

		output, err = executeFaucetWithTokens(t, configPath, 1.0)
		require.Nil(t, err, "faucet execution failed", strings.Join(output, "\n"))

		// Lock 0.5 token for allocation
		allocParams := createParams(map[string]interface{}{
			"expire": "5m",
			"size":   "1024",
			"lock":   "0.5",
		})
		output, err = createNewAllocation(t, configPath, allocParams)
		require.Nil(t, err, "Failed to create new allocation", strings.Join(output, "\n"))

		require.Len(t, output, 1)
		require.Regexp(t, regexp.MustCompile("Allocation created: ([a-f0-9]{64})"), output[0], "Allocation creation output did not match expected")
		allocationID := strings.Fields(output[0])[2]

		// Not specifying amount to lock should not succeed
		params := createParams(map[string]interface{}{
			"allocation": allocationID,
			"tokens":     "0.5",
		})
		output, err = writePoolLock(t, configPath, params, false)
		require.NotNil(t, err, "Locked tokens without providing amount to lock", strings.Join(output, "\n"))
		require.Len(t, output, 1)
		require.Equal(t, "missing required 'duration' flag", output[0])
	})

	t.Run("Should not be able to unlock unexpired write tokens", func(t *testing.T) {
		t.Parallel()

		output, err := registerWallet(t, configPath)
		require.Nil(t, err, "registering wallet failed", strings.Join(output, "\n"))

		output, err = executeFaucetWithTokens(t, configPath, 2.0)
		require.Nil(t, err, "faucet execution failed", strings.Join(output, "\n"))

		// Lock 0.5 token for allocation
		allocParams := createParams(map[string]interface{}{
			"expire": "30m",
			"size":   "1024",
			"lock":   "0.5",
		})
		output, err = createNewAllocation(t, configPath, allocParams)
		require.Nil(t, err, "Failed to create new allocation", strings.Join(output, "\n"))

		require.Len(t, output, 1)
		require.Regexp(t, regexp.MustCompile("Allocation created: ([a-f0-9]{64})"), output[0], "Allocation creation output did not match expected")
		allocationID := strings.Fields(output[0])[2]

		// Lock 1 token in write pool distributed amongst all blobbers
		params := createParams(map[string]interface{}{
			"allocation": allocationID,
			"tokens":     1,
			"duration":   "30m",
		})
		output, err = writePoolLock(t, configPath, params, true)
		require.Nil(t, err, "Tokens could not be locked", strings.Join(output, "\n"))

		require.Len(t, output, 1)
		require.Equal(t, "locked", output[0])

		cliutils.Wait(t, 5*time.Second)

		output, err = writePoolInfo(t, configPath, true)
		require.Len(t, output, 1, strings.Join(output, "\n"))
		require.Nil(t, err, "error fetching write pool info", strings.Join(output, "\n"))

		writePools := []climodel.WritePoolInfo{}
		err = json.Unmarshal([]byte(output[0]), &writePools)
		require.Nil(t, err, "Error unmarshalling write pool", strings.Join(output, "\n"))
		require.NotEmpty(t, writePools)

		// Unlock without waiting till expiration should result in error
		require.True(t, len(writePools) >= 2, "number of write pools did not math expected")
		customWritePoolId := writePools[0].Id
		if customWritePoolId == allocationID {
			customWritePoolId = writePools[1].Id
		}
		params = createParams(map[string]interface{}{
			"pool_id": customWritePoolId,
		})
		output, err = writePoolUnlock(t, configPath, params, false)
		require.NotNil(t, err, "Write pool tokens unlocked before expired", strings.Join(output, "\n"))

		require.True(t, len(output) > 0, "expected output length be at least 1")
		require.Equal(t, "Failed to unlock tokens in write pool: write_pool_unlock_failed: the pool is not expired yet", output[0])
	})

	// Possible FIXME: Locking write tokens for duration more than allocation's duration
	// is possible but shouldn't be/should warn the user
	t.Run("Locking write tokens for duration more than allocation's expiration should fail/should warn the user", func(t *testing.T) {
		t.Parallel()

		output, err := registerWallet(t, configPath)
		require.Nil(t, err, "registering wallet failed", strings.Join(output, "\n"))

		output, err = executeFaucetWithTokens(t, configPath, 2.0)
		require.Nil(t, err, "faucet execution failed", strings.Join(output, "\n"))

		// Lock 0.5 token for allocation
		allocParams := createParams(map[string]interface{}{
			"expire": "5m",
			"size":   "1024",
			"lock":   "0.5",
		})
		output, err = createNewAllocation(t, configPath, allocParams)
		require.Nil(t, err, "Failed to create new allocation", strings.Join(output, "\n"))

		require.Len(t, output, 1)
		require.Regexp(t, regexp.MustCompile("Allocation created: ([a-f0-9]{64})"), output[0], "Allocation creation output did not match expected")
		allocationID := strings.Fields(output[0])[2]

		// Lock 1 token in write pool distributed amongst all blobbers
		params := createParams(map[string]interface{}{
			"allocation": allocationID,
			"tokens":     1,
			"duration":   "10m",
		})
		output, err = writePoolLock(t, configPath, params, true)
		// TODO: change if FIXME is implemented
		require.Nil(t, err, "Tokens could not be locked", strings.Join(output, "\n"))

		require.Len(t, output, 1)
		require.Equal(t, "locked", output[0])
	})
}

func writePoolLock(t *testing.T, cliConfigFilename, params string, retry bool) ([]string, error) {
	return writePoolLockWithWallet(t, escapedTestName(t), cliConfigFilename, params, retry)
}

func writePoolLockWithWallet(t *testing.T, wallet, cliConfigFilename, params string, retry bool) ([]string, error) {
	t.Logf("Locking write tokens...")
	cmd := fmt.Sprintf("./zbox wp-lock %s --silent --wallet %s_wallet.json --configDir ./config --config %s", params, wallet, cliConfigFilename)
	if retry {
		return cliutils.RunCommand(t, cmd, 3, time.Second*2)
	} else {
		return cliutils.RunCommandWithoutRetry(cmd)
	}
}

func writePoolUnlock(t *testing.T, cliConfigFilename, params string, retry bool) ([]string, error) {
	t.Logf("Unlocking write tokens...")
	cmd := fmt.Sprintf("./zbox wp-unlock %s --silent --wallet %s_wallet.json --configDir ./config --config %s", params, escapedTestName(t), cliConfigFilename)
	if retry {
		return cliutils.RunCommand(t, cmd, 3, time.Second*2)
	} else {
		return cliutils.RunCommandWithoutRetry(cmd)
	}
}
