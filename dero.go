package derogo

import (
	"fmt"
	"strings"
	"strconv"
	"os"
	"errors"
	"time"
	"context"
	"math"
	"math/big"
	"encoding/hex"
	"encoding/json"

	"github.com/deroproject/derohe/rpc"
        "github.com/deroproject/derohe/glue/rwc"
	"github.com/deroproject/derohe/walletapi"
	"github.com/deroproject/derohe/cryptography/crypto"
	"github.com/deroproject/derohe/globals"
	"github.com/deroproject/derohe/block"
	"github.com/deroproject/derohe/config"
        "github.com/creachadair/jrpc2"
        "github.com/creachadair/jrpc2/channel"
        "github.com/gorilla/websocket"
)

type DeroClient struct {
        WS  *websocket.Conn
        RPC *jrpc2.Client
}

var deroNode *DeroClient
var w *walletapi.Wallet_Memory
var deroNodeAddr string

func (cli *DeroClient) IsDaemonOnline() bool {
        if cli.WS == nil || cli.RPC == nil {
                return false
        }
        return true
}

func (cli *DeroClient) Call(method string, params interface{}, result interface{}) error {
        try := 0
try_again:
        if cli == nil || !cli.IsDaemonOnline() {
                deroConnect()
                time.Sleep(time.Second)
                try++
                if try < 3 {
                        goto try_again
                }
                return fmt.Errorf("client is offline or not connected")
        }
        return cli.RPC.CallResult(context.Background(), method, params, result)
}

func DeroSendRegTxn(data []byte) (error) {
	params := rpc.SendRawTransaction_Params{Tx_as_hex: hex.EncodeToString(data)}
	var result rpc.SendRawTransaction_Result

	return deroNode.Call("DERO.SendRawTransaction", params, &result)
}

func deroConnect() (err error) {
        deroNode.WS, _, err = websocket.DefaultDialer.Dial("ws://"+deroNodeAddr+"/ws", nil)
	if err != nil {
		return err
	}

        input_output := rwc.New(deroNode.WS)
        deroNode.RPC = jrpc2.NewClient(channel.RawJSON(input_output, input_output), nil)

        return nil
}

func DeroInit(node string) (*DeroClient) {
	deroNodeAddr = node

	if deroNode == nil {
		deroNode = &DeroClient{}
		err := deroConnect()
		if err != nil {
			fmt.Printf("Error connecting to node: %s\n", err)
			os.Exit(-1)
		}

		go deroNode.deroKeepConnected()
	}

	return deroNode
}

func (cli *DeroClient) deroKeepConnected() {
	for {
		if cli.IsDaemonOnline() {
			var result string
			if err := cli.Call("DERO.Ping", nil, &result); err != nil {
				fmt.Printf("Ping failed: %s\n", err)
				cli.RPC.Close()
				cli.WS = nil
				cli.RPC = nil
				deroConnect() // try to connect again
			} else {
//				fmt.Printf("Ping Received %s\n", result)
			}
		}
		time.Sleep(time.Second)
	}
}

func DeroWalletInit(node string, mainnet bool, wallet string, password string) (*walletapi.Wallet_Memory) {
	globals.Arguments["--daemon-address"] = node
	globals.Arguments["--testnet"] = !mainnet
	globals.InitNetwork()

	var err error
	var wdisk *walletapi.Wallet_Disk
	if _, err = os.Stat(wallet); errors.Is(err, os.ErrNotExist) {
		if len(wallet) == 64 {
			var seed []byte
			seed, err = hex.DecodeString(wallet)
			if err == nil {
				w, err = walletapi.Create_Encrypted_Wallet_Memory("wallet", new(crypto.BNRed).SetBytes(seed))
			}
		}
	} else {
		wdisk, err = walletapi.Open_Encrypted_Wallet(wallet, password)
		if err == nil {
			w = wdisk.Wallet_Memory
		}
	}

	if err != nil {
		fmt.Printf("Error opening wallet: %s\n", err)
		os.Exit(-1)
	}

	w.SetNetwork(mainnet)
	w.SetOnlineMode();

	if wdisk != nil && !w.IsRegistered() {
		fmt.Printf("Wallet not registered!\n")
	}

	go walletapi.Keep_Connectivity()

	walletapi.Initialize_LookupTable(1, 1<<22)

	return w
}

func DeroTransfer(transfers []rpc.Transfer) (string, bool) {
	var p rpc.Transfer_Params
	p.Transfers = transfers
	p.Ringsize = 16

	return deroTransfer(p)
}

func deroTransfer(p rpc.Transfer_Params) (string, bool) {
	res := true
	txid := ""

	tx, err := w.TransferPayload0(p.Transfers, p.Ringsize, false, p.SC_RPC, p.Fees, false)

	if err != nil {
		fmt.Printf("Error building tx: %s\n", err)
		res = false
	} else {
		txid = tx.GetHash().String();
		err = w.SendTransaction(tx)

		if err != nil {
			fmt.Printf("deroTransfer ERROR: %s\n", err)
			res = false
		}
	}

	return txid, res
}

func DeroBuildTransfers(transfers []rpc.Transfer, SCID string, destination string, amount uint64, burn uint64) ([]rpc.Transfer) {
	var t rpc.Transfer

	if len(SCID) > 0 {
		t.SCID = crypto.HashHexToHash(SCID)
	}

	t.Destination = destination
	if len(destination) == 0 {
		t.Destination = DeroGetRandomAddress()
	}
	t.Amount = amount
	t.Burn = burn

	transfers = append(transfers, t)
	return transfers
}

func DeroSafeDeploy(src []byte, args rpc.Arguments) (string, bool) {
        var p rpc.Transfer_Params

        p.SC_Code = string(src)
        p.Ringsize = 2
        p.Signer = DeroGetAddress().String()
        p.SC_RPC = args
        p.SC_RPC = append(p.SC_RPC, rpc.Argument{Name: rpc.SCACTION, DataType: rpc.DataUint64, Value: uint64(rpc.SC_INSTALL)})
        p.SC_RPC = append(p.SC_RPC, rpc.Argument{Name: rpc.SCCODE, DataType: rpc.DataString, Value: p.SC_Code})

        var g = rpc.GasEstimate_Params(p)
        var r rpc.GasEstimate_Result
        var valid = false

        err := deroNode.Call("DERO.GetGasEstimate", g, &r)
        if err != nil {
                fmt.Printf("DERO.GetGasEstimate error: %s\n", err)
        } else  {
                valid = true
        }

        if valid  && r.Status == "OK" {
		p.Fees = r.GasStorage
                return deroTransfer(p)
        }

        return "", false
}

func DeroDeploySC(code []byte) (string, bool) {
        var p rpc.Transfer_Params

	p.SC_Code = string(code)
	p.Ringsize = 2
	p.SC_RPC = append(p.SC_RPC, rpc.Argument{Name: rpc.SCACTION, DataType: rpc.DataUint64, Value: uint64(rpc.SC_INSTALL)})
	p.SC_RPC = append(p.SC_RPC, rpc.Argument{Name: rpc.SCCODE, DataType: rpc.DataString, Value: p.SC_Code})

	return deroTransfer(p)
}

func DeroGetBlock(blockHeight uint64) (value block.Block, valid bool) {
	valid = true

	p := rpc.GetBlock_Params{Height: blockHeight}
	r := rpc.GetBlock_Result{}

	err := deroNode.Call("DERO.GetBlock", p, &r)
	if err != nil {
//		fmt.Printf("DERO.GetBlock error: %s\n", err)
		valid = false
	} else  {
		json.Unmarshal([]byte(r.Json), &value)
	}

	return
}

func DeroGetVars(SCID string) (variables map[string]interface{}, valid bool) {
	valid = true

	var p = rpc.GetSC_Params{SCID: SCID, Variables: true, Code: false}
	var r rpc.GetSC_Result

	err := deroNode.Call("DERO.GetSC", p, &r)
	if err != nil {
		fmt.Printf("DERO.GetSC error: %s\n", err)
		valid = false
	} else  {
		variables = r.VariableStringKeys;
	}

	return
}

func DeroGetVar(SCID string, variable string) (value string, valid bool) {
	valid = true

	var p = rpc.GetSC_Params{SCID: SCID, Variables: false, Code: false, KeysString: []string{variable}}
	var r rpc.GetSC_Result

	err := deroNode.Call("DERO.GetSC", p, &r)
	if err != nil {
		fmt.Printf("DERO.GetSC error: %s\n", err)
		valid = false
	} else  {
		value = r.ValuesString[0];
		if strings.Contains(value, "NOT AVAILABLE") {
			value = ""
		}
	}

	return
}

func DeroGetTx(txHash string) (r rpc.GetTransaction_Result, valid bool) {
	valid = true

	p := rpc.GetTransaction_Params{Tx_Hashes: []string{txHash}}

	err := deroNode.Call("DERO.GetTransaction", p, &r)
	if err != nil {
		fmt.Printf("DERO.GetTransaction error: %s\n", err)
		valid = false
	}

	return
}

func DeroGetTxInfo(txHash string) (rpc.Tx_Related_Info, bool) {
	value, valid := DeroGetTx(txHash)

	ret := rpc.Tx_Related_Info{}

	if valid {
		ret = value.Txs[0]
	}

	return ret, valid
}

func DeroConfirmTx(txid string, settleBlocks uint64) (restult string) {
	tx, valid := DeroGetTxInfo(txid)

	if !valid {
		return "error"
	}

	if uint64(tx.Block_Height) + settleBlocks > DeroGetHeight() {
		return "pending"
	}

	if tx.In_pool {
		return "pending"
	}

	if tx.Block_Height > 0 {
		return "confirmed"
	}

	return "failed"
}

func deroBuildCall(SCID string, transfers []rpc.Transfer, args rpc.Arguments, fees uint64) (rpc.Transfer_Params) {
	var p rpc.Transfer_Params
	p.Transfers = transfers
	p.SC_ID = SCID
	p.SC_RPC = args
	p.Ringsize = 2
	p.Signer = DeroGetAddress().String()

	p.Fees = fees

	p.SC_RPC = append(p.SC_RPC, rpc.Argument{Name: rpc.SCACTION, DataType: rpc.DataUint64, Value: uint64(rpc.SC_CALL)})
	p.SC_RPC = append(p.SC_RPC, rpc.Argument{Name: rpc.SCID, DataType: rpc.DataHash, Value: crypto.HashHexToHash(p.SC_ID)})

	return p
}

func DeroCallSC(SCID string, transfers []rpc.Transfer, args rpc.Arguments, fees uint64) (string, bool) {
	p := deroBuildCall(SCID, transfers, args, fees)

	return deroTransfer(p)
}

func DeroSafeCallSC(scid string, transfers []rpc.Transfer, args rpc.Arguments) (string, bool) {
        res, res_valid := DeroEstimateGas(scid, transfers, args, 0)
        if !res_valid {
                return "", false
        }

        if res.Status != "OK" {
                return res.Status, false
        }

	fee := res.GasStorage
	txid, txid_valid := DeroCallSC(scid, transfers, args, fee)

	if !txid_valid {
		fmt.Printf("Retrying with higher fee...\n")
		// round up, try again
		div := (fee-1) / config.FEE_PER_KB
		fee = (div+1) * config.FEE_PER_KB

		txid, txid_valid = DeroCallSC(scid, transfers, args, fee)
	}

	return txid, txid_valid
}

func DeroEstimateGas(SCID string, transfers []rpc.Transfer, args rpc.Arguments, fees uint64) (rpc.GasEstimate_Result, bool) {
	var p = rpc.GasEstimate_Params(deroBuildCall(SCID, transfers, args, fees))
        var r rpc.GasEstimate_Result
	var valid = false

        err := deroNode.Call("DERO.GetGasEstimate", p, &r)
        if err != nil {
                fmt.Printf("DERO.GetGasEstimate error: %s\n", err)
        } else  {
                valid = true
        }

	return r, valid
}

func DeroGetRandomAddress() (string) {
	var p = rpc.GetRandomAddress_Params{}
	var r rpc.GetRandomAddress_Result

	for len(r.Address) < 1 {
		deroNode.Call("DERO.GetRandomAddress", p, &r)
	}

	return r.Address[0]
}

func DeroGetSCBal(scid string) uint64 {
	bal, _, _ := w.GetDecryptedBalanceAtTopoHeight(crypto.HashHexToHash(scid), -1, w.GetAddress().String())

	return bal
}

func DeroFormatMoneyPrecision(amount uint64, decimals int) (*big.Float) {
	a := new(big.Float).SetUint64(amount)
	divisor := uint64(math.Pow(10, float64(decimals)))
	a.Quo(a, new(big.Float).SetUint64(divisor))

	return a
}

func DeroStringToAmount(str string, decimals int) (uint64, error) {
	value, err := strconv.ParseFloat(str, 64)

	if err == nil {
		f := new(big.Float).SetFloat64(value)
		mul := new(big.Float).SetUint64(uint64(math.Pow(10, float64(decimals))))
		f.Mul(f, mul)
		i, _ := f.Int(nil)

		return i.Uint64(), nil
	}

	return 0, err
}

func DeroGetBalance() uint64 {
//	bal, _ := w.Get_Balance()
	bal, _ := w.Get_Balance_Rescan()

	return bal
}

func DeroGetAddress() rpc.Address {
	return w.GetAddress()
}

func DeroParseValidateAddress(a string) (addr *rpc.Address, err error) {
	return globals.ParseValidateAddress(a)
}

func DeroGetInfo() (info rpc.GetInfo_Result) {
	deroNode.Call("DERO.GetInfo", nil, &info)
	return
}

func DeroGetHeight() uint64 {
	info := DeroGetInfo()
	return uint64(info.Height)
}

func DeroGetWalletHeight() uint64 {
	return w.Get_Height()
}

func DeroGetPub() []byte {
	keys :=  w.Get_Keys()
	return []byte(keys.Public.String())
}

func DeroGetPriv() []byte {
	keys :=  w.Get_Keys()
	return []byte(keys.Secret.String())
}

func DeroMinerAddressToPubKey(minerAddr [33]byte) string {
	var acckey crypto.Point
	acckey.DecodeCompressed(minerAddr[:])
	astring := rpc.NewAddressFromKeys(&acckey)

	return astring.String()
}

func DeroInitLookupTable(count int, table_size int) {
        walletapi.Initialize_LookupTable(count, table_size)
}

var keystoreSCID string

func DeroGetKeystoreSCID() (scid string, valid bool) {
	scid = keystoreSCID
	valid = true

	if len(scid) == 0 {
		var zerohash crypto.Hash
		zerohash[31] = 1

		scid, valid = DeroGetVar(hex.EncodeToString(zerohash[:]), "keystore")

		valid = (valid && len(scid) > 0)

		if valid {
			scid = "80" + scid[2:64]
		}
	}

	return
}

func DeroGetKey(key string) (r rpc.GetSC_Result, valid bool) {
	scid, scid_valid := DeroGetKeystoreSCID()

	if scid_valid {
		var p = rpc.GetSC_Params{SCID: scid, Variables: false, Code: false, KeysString: []string{"k:" + key}}

		err := deroNode.Call("DERO.GetSC", p, &r)
		if err == nil {
//			s, _ := json.MarshalIndent(r, "", "\t")
//			fmt.Printf("%s\n", string(s))
			valid = true
		}
	}

	return
}

func DeroGetKeyString(key string) (value string, valid bool) {
	r, r_valid := DeroGetKey(key)

        if r_valid {
                str := r.ValuesString[0]
		bytes, _ := hex.DecodeString(str)
		value = string(bytes)
		valid = !strings.Contains(str, "NOT AVAILABLE")
        }

        return
}

func DeroGetKeyHex(key string) (value string, valid bool) {
	r, r_valid := DeroGetKey(key)

        if r_valid {
                value = r.ValuesString[0]
		valid = !strings.Contains(value, "NOT AVAILABLE")
        }

        return
}

func DeroGetKeyUint64(key string) (value uint64, valid bool) {
	r, r_valid := DeroGetKey(key)

        if r_valid {
                str := r.ValuesString[0]
                num, _ := strconv.Atoi(str)
		value = uint64(num)
		valid = !strings.Contains(str, "NOT AVAILABLE")
        }

        return
}

func DeroStoreKeyString(key string, value string) (txid string, valid bool) {
        var transfers []rpc.Transfer
        var args rpc.Arguments
        args = append(args, rpc.Argument {"entrypoint", rpc.DataString, "StoreKeyString"})
        args = append(args, rpc.Argument {"k", rpc.DataString, key})
        args = append(args, rpc.Argument {"v", rpc.DataString, value})

        scid, scid_valid := DeroGetKeystoreSCID()
	if scid_valid {
		txid, valid = DeroSafeCallSC(scid, transfers, args)
	}

	return
}

func DeroStoreKeyHex(key string, value string) (txid string, valid bool) {
        var transfers []rpc.Transfer
        var args rpc.Arguments
        args = append(args, rpc.Argument {"entrypoint", rpc.DataString, "StoreKeyHex"})
        args = append(args, rpc.Argument {"k", rpc.DataString, key})
        args = append(args, rpc.Argument {"v", rpc.DataString, value})

        scid, scid_valid := DeroGetKeystoreSCID()
	if scid_valid {
		txid, valid = DeroSafeCallSC(scid, transfers, args)
	}

	return
}

func DeroStoreKeyUint64(key string, value uint64) (txid string, valid bool) {
        var transfers []rpc.Transfer
        var args rpc.Arguments
        args = append(args, rpc.Argument {"entrypoint", rpc.DataString, "StoreKeyUint64"})
        args = append(args, rpc.Argument {"k", rpc.DataString, key})
        args = append(args, rpc.Argument {"v", rpc.DataUint64, value})

        scid, scid_valid := DeroGetKeystoreSCID()
	if scid_valid {
		txid, valid = DeroSafeCallSC(scid, transfers, args)
	}

	return
}

func DeroLockKey(key string) (txid string, valid bool) {
        var transfers []rpc.Transfer
        var args rpc.Arguments
        args = append(args, rpc.Argument {"entrypoint", rpc.DataString, "LockKey"})
        args = append(args, rpc.Argument {"k", rpc.DataString, key})

        scid, scid_valid := DeroGetKeystoreSCID()
	if scid_valid {
		txid, valid = DeroSafeCallSC(scid, transfers, args)
	}

	return
}

func DeroDeleteKey(key string) (txid string, valid bool) {
        var transfers []rpc.Transfer
        var args rpc.Arguments
        args = append(args, rpc.Argument {"entrypoint", rpc.DataString, "DeleteKey"})
        args = append(args, rpc.Argument {"k", rpc.DataString, key})

        scid, scid_valid := DeroGetKeystoreSCID()
	if scid_valid {
		txid, valid = DeroSafeCallSC(scid, transfers, args)
	}

	return
}
