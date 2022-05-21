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
        "github.com/creachadair/jrpc2"
        "github.com/creachadair/jrpc2/channel"
        "github.com/gorilla/websocket"
)

type deroClient struct {
        WS  *websocket.Conn
        RPC *jrpc2.Client
}

var deroNode *deroClient
var w *walletapi.Wallet_Memory
var deroNodeAddr string

func (cli *deroClient) IsDaemonOnline() bool {
        if cli.WS == nil || cli.RPC == nil {
                return false
        }
        return true
}

func (cli *deroClient) call(method string, params interface{}, result interface{}) error {
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

func deroConnect() (err error) {
        deroNode.WS, _, err = websocket.DefaultDialer.Dial("ws://"+deroNodeAddr+"/ws", nil)
	if err != nil {
		return err
	}

        input_output := rwc.New(deroNode.WS)
        deroNode.RPC = jrpc2.NewClient(channel.RawJSON(input_output, input_output), nil)

        return nil
}

func DeroInit(node string) {
	deroNodeAddr = node

	if deroNode == nil {
		deroNode = &deroClient{}
		err := deroConnect()
		if err != nil {
			fmt.Printf("Error connecting to node: %s\n", err)
			os.Exit(-1)
		}

		go deroNode.deroKeepConnected()
	}
}

func (cli *deroClient) deroKeepConnected() {
	for {
		if cli.IsDaemonOnline() {
			var result string
			if err := cli.call("DERO.Ping", nil, &result); err != nil {
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

func DeroWalletInit(node string, mainnet bool, wallet string, password string) {
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
				w, err = walletapi.Create_Encrypted_Wallet_Memory("test", new(crypto.BNRed).SetBytes(seed))
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

	w.SetOnlineMode();

	if wdisk != nil && !w.IsRegistered() {
		fmt.Printf("Wallet not registered!\n")
	}

	go walletapi.Keep_Connectivity()

	walletapi.Initialize_LookupTable(1, 1<<22)
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

func DeroDeploySC(code []byte) (string, bool) {
        var p rpc.Transfer_Params

	p.SC_Code = string(code)
	p.Ringsize = 2
	p.SC_RPC = append(p.SC_RPC, rpc.Argument{Name: rpc.SCACTION, DataType: rpc.DataUint64, Value: uint64(rpc.SC_INSTALL)})
	p.SC_RPC = append(p.SC_RPC, rpc.Argument{Name: rpc.SCCODE, DataType: rpc.DataString, Value: p.SC_Code})

	return deroTransfer(p)
}

func DeroGetBlock(blockHeight uint64) (block.Block, bool) {
	valid := true
	value := block.Block{}

	p := rpc.GetBlock_Params{Height: blockHeight}
	r := rpc.GetBlock_Result{}

	err := deroNode.call("DERO.GetBlock", p, &r)
	if err != nil {
//		fmt.Printf("DERO.GetBlock error: %s\n", err)
		valid = false
	} else  {
		json.Unmarshal([]byte(r.Json), &value)
	}

	return value, valid
}

func DeroGetVars(SCID string) (map[string]interface{}, bool) {
	var variables map[string]interface{}
	valid := true

	var p = rpc.GetSC_Params{SCID: SCID, Variables: true, Code: false}
	var r rpc.GetSC_Result

	err := deroNode.call("DERO.GetSC", p, &r)
	if err != nil {
		fmt.Printf("DERO.GetSC error: %s\n", err)
		valid = false
	} else  {
//s, _ := json.MarshalIndent(r, "", "\t")
//fmt.Print(string(s))
		variables = r.VariableStringKeys;
	}

	return variables, valid
}

func DeroGetVar(SCID string, variable string) (string, bool) {
	valid := true
	value := ""

	var p = rpc.GetSC_Params{SCID: SCID, Variables: false, Code: false, KeysString: []string{variable}}
	var r rpc.GetSC_Result

	err := deroNode.call("DERO.GetSC", p, &r)
	if err != nil {
		fmt.Printf("DERO.GetSC error: %s\n", err)
		valid = false
	} else  {
		value = r.ValuesString[0];
		if strings.Contains(value, "NOT AVAILABLE") {
			value = ""
		}
	}

	return value, valid
}

func DeroGetTx(txHash string) (rpc.GetTransaction_Result, bool) {
	valid := true

	p := rpc.GetTransaction_Params{Tx_Hashes: []string{txHash}}
	r := rpc.GetTransaction_Result{}

	err := deroNode.call("DERO.GetTransaction", p, &r)
	if err != nil {
		fmt.Printf("DERO.GetTransaction error: %s\n", err)
		valid = false
	}

	return r, valid
}

func DeroGetTxInfo(txHash string) (rpc.Tx_Related_Info, bool) {
	value, valid := DeroGetTx(txHash)

	ret := rpc.Tx_Related_Info{}

	if valid {
		ret = value.Txs[0]
	}

	return ret, valid
}

func DeroConfirmTx(txid string) (restult string) {
	tx, valid := DeroGetTxInfo(txid)

	if !valid {
		return "error"
	}

	if tx.In_pool {
		return "pending"
	}

	if tx.Block_Height > 0 {
		return "confirmed"
	}

	return "failed"
}

func DeroCallSC(SCID string, transfers []rpc.Transfer, args rpc.Arguments) (string, bool) {
	var p rpc.Transfer_Params
	p.Transfers = transfers
	p.SC_ID = SCID
	p.SC_RPC = args
	p.Ringsize = 2
	p.SC_RPC = args

	p.SC_RPC = append(p.SC_RPC, rpc.Argument{Name: rpc.SCACTION, DataType: rpc.DataUint64, Value: uint64(rpc.SC_CALL)})
	p.SC_RPC = append(p.SC_RPC, rpc.Argument{Name: rpc.SCID, DataType: rpc.DataHash, Value: crypto.HashHexToHash(p.SC_ID)})

	return deroTransfer(p)
}

func DeroGetRandomAddress() (string) {
	var p = rpc.GetRandomAddress_Params{}
	var r rpc.GetRandomAddress_Result

	for len(r.Address) < 1 {
		deroNode.call("DERO.GetRandomAddress", p, &r)
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

func DeroGetHeight() uint64 {
	return w.Get_Daemon_Height()
}

func DeroGetWalletHeight() uint64 {
	return w.Get_Height()
}

