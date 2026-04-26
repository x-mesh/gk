package cli

// init_config.go — 이 파일은 기존 `gk init config` 등록을 담당했으나,
// 해당 기능은 config_init.go (gk config init)로 이동되었다.
// deprecated alias는 init.go에서 처리한다.
//
// 이 파일은 기존 테스트 호환성을 위해 유지되며,
// runInitConfig 함수는 runConfigInit으로 위임한다.

// runInitConfig은 기존 테스트 호환성을 위한 wrapper이다.
// 실제 로직은 config_init.go의 runConfigInit에 있다.
var runInitConfig = runConfigInit
