package main

import (
 "bufio"
 "encoding/hex"
 "fmt"
 "os"
 arklib "github.com/arkade-os/arkd/pkg/ark-lib"
 "github.com/arkade-os/arkd/pkg/ark-lib/script"
 "github.com/btcsuite/btcd/btcec/v2"
 "github.com/btcsuite/btcd/btcutil/hdkeychain"
 "github.com/btcsuite/btcd/chaincfg"
 "github.com/tyler-smith/go-bip39"
)

func main() {
 input:=bufio.NewScanner(os.Stdin); if !input.Scan(){panic("input")}
 seed,err:=bip39.NewSeedWithErrorChecking(input.Text(),"");if err!=nil{panic("mnemonic")};defer clear(seed)
 key,err:=hdkeychain.NewMaster(seed,&chaincfg.TestNet3Params);if err!=nil{panic(err)};defer key.Zero()
 for _,i:=range []uint32{86+hdkeychain.HardenedKeyStart,1+hdkeychain.HardenedKeyStart,hdkeychain.HardenedKeyStart,0}{
  key,err=key.Derive(i);if err!=nil{panic(err)};defer key.Zero()
 }
 serverBytes,_:=hex.DecodeString("03301078808e4f7bc0dadfe29e34b1df8eaf0108ef06b1722274075ebc107a127a");server,_:=btcec.ParsePubKey(serverBytes)
 delegateBytes,_:=hex.DecodeString("032903b15efe236d9609da10e536fb32cdf1d144778797bbf32a9b94e86601be6a");delegate,_:=btcec.ParsePubKey(delegateBytes)
 for index:=uint32(0);index<100;index++ {
  child,err:=key.Derive(index);if err!=nil{panic(err)}
  owner,err:=child.ECPubKey();if err!=nil{panic(err)};child.Zero()
  for _,seconds:=range []uint32{2048,4096}{
   tree:=script.NewDefaultVtxoScript(owner,server,arklib.RelativeLocktime{Type:arklib.LocktimeTypeSecond,Value:seconds})
   tree.Closures=append(tree.Closures,&script.MultisigClosure{PubKeys:[]*btcec.PublicKey{owner,delegate,server}})
   output,_,err:=tree.TapTree();if err!=nil{panic(err)}
   address:=arklib.Address{Version:0,HRP:"tark",Signer:server,VtxoTapKey:output};encoded,err:=address.EncodeV0();if err!=nil{panic(err)}
   if index<3 || encoded=="tark1qqcpq7yq3e8hhsx6ml3fud93m7827qggaurtzu3zwsr4a0qs0gf84xa5h7g03tgx70g6d09fmpdqyejl54tqp604n6mqc3fjeuprg036yvvvst" { fmt.Printf("index=%d owner=%x delay=%d address=%s\n",index,owner.SerializeCompressed(),seconds,encoded) }
  }
 }
}
