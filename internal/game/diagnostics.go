package game

import "strconv"

// Diagnostic names include only enum metadata, never event/effect payloads.
func (v Stage) diagnosticName() string {
	names := [...]string{
		StageInit:                       "StageInit",
		StageSessionPrepared:            "StageSessionPrepared",
		StagePlayer2KeysPrepared:        "StagePlayer2KeysPrepared",
		StageAwaitOpponentKeys:          "StageAwaitOpponentKeys",
		StagePlayer1KeysPrepared:        "StagePlayer1KeysPrepared",
		StageInitialShuffle:             "StageInitialShuffle",
		StageInitialShufflePrepared:     "StageInitialShufflePrepared",
		StageAwaitInitialShuffle:        "StageAwaitInitialShuffle",
		StageFinalShuffle:               "StageFinalShuffle",
		StageFinalShufflePrepared:       "StageFinalShufflePrepared",
		StageAwaitFinalShuffle:          "StageAwaitFinalShuffle",
		StageInitialDeposit:             "StageInitialDeposit",
		StageAwaitInitialDeposit:        "StageAwaitInitialDeposit",
		StageAborted:                    "StageAborted",
		StagePlayer1Funding:             "StagePlayer1Funding",
		StageAwaitPlayer1Funding:        "StageAwaitPlayer1Funding",
		StagePlayer2Opening:             "StagePlayer2Opening",
		StageAwaitPlayer2Opening:        "StageAwaitPlayer2Opening",
		StageBettingLocal:               "StageBettingLocal",
		StageBettingOpponent:            "StageBettingOpponent",
		StageBoardRevealLocal:           "StageBoardRevealLocal",
		StageBoardRevealOpponent:        "StageBoardRevealOpponent",
		StageAllInLocal:                 "StageAllInLocal",
		StageAllInOpponent:              "StageAllInOpponent",
		StageAllInRevealLocal:           "StageAllInRevealLocal",
		StageAllInRevealOpponent:        "StageAllInRevealOpponent",
		StageShowdownLocal:              "StageShowdownLocal",
		StageShowdownOpponent:           "StageShowdownOpponent",
		StageEvaluateShowdown:           "StageEvaluateShowdown",
		StageSettleShowdown:             "StageSettleShowdown",
		StageTransactionPrepared:        "StageTransactionPrepared",
		StageTransactionSigned:          "StageTransactionSigned",
		StageAwaitTransactionAcceptance: "StageAwaitTransactionAcceptance",
		StageFinished:                   "StageFinished",
	}
	if int(v) < len(names) && names[v] != "" {
		return names[v]
	}
	return strconv.Itoa(int(v))
}

func (v InputKind) diagnosticName() string {
	names := [...]string{
		StartSession:   "StartSession",
		JoinSession:    "JoinSession",
		Progress:       "Progress",
		Bet:            "Bet",
		Concede:        "Concede",
		RevealShowdown: "RevealShowdown",
		ClaimTimeout:   "ClaimTimeout",
	}
	if int(v) < len(names) && names[v] != "" {
		return names[v]
	}
	return strconv.Itoa(int(v))
}

func (v EventKind) diagnosticName() string {
	names := [...]string{
		Configured:          "Configured",
		SessionPrepared:     "SessionPrepared",
		MessagePrepared:     "MessagePrepared",
		MessagePublished:    "MessagePublished",
		MessageReceived:     "MessageReceived",
		SpendPrepared:       "SpendPrepared",
		SpendSigned:         "SpendSigned",
		SpendObserved:       "SpendObserved",
		SubmissionFailed:    "SubmissionFailed",
		SessionOpened:       "SessionOpened",
		SetupAborted:        "SetupAborted",
		DepositObserved:     "DepositObserved",
		OpeningObserved:     "OpeningObserved",
		ShowdownEvaluated:   "ShowdownEvaluated",
		SubmissionAttempted: "SubmissionAttempted",
		PublicationPrepared: "PublicationPrepared",
	}
	if int(v) < len(names) && names[v] != "" {
		return names[v]
	}
	return strconv.Itoa(int(v))
}

func (v EffectKind) diagnosticName() string {
	names := [...]string{
		PrepareSessionEffect:       "PrepareSessionEffect",
		OpenSessionEffect:          "OpenSessionEffect",
		PublishMessageEffect:       "PublishMessageEffect",
		ReceiveMessageEffect:       "ReceiveMessageEffect",
		PrepareShuffleEffect:       "PrepareShuffleEffect",
		WatchAndPublishFinalEffect: "WatchAndPublishFinalEffect",
		PrepareDepositEffect:       "PrepareDepositEffect",
		DiscoverDepositEffect:      "DiscoverDepositEffect",
		ObserveLocalActionEffect:   "ObserveLocalActionEffect",
		WaitOpponentEffect:         "WaitOpponentEffect",
		PrepareOpeningEffect:       "PrepareOpeningEffect",
		EvaluateShowdownEffect:     "EvaluateShowdownEffect",
		WaitSettlementEffect:       "WaitSettlementEffect",
		SignPreparedEffect:         "SignPreparedEffect",
		SubmitSignedEffect:         "SubmitSignedEffect",
		ReconcileSubmissionEffect:  "ReconcileSubmissionEffect",
	}
	if int(v) < len(names) && names[v] != "" {
		return names[v]
	}
	return strconv.Itoa(int(v))
}
