package cli

// Workflow는 gk guide에서 제공하는 워크플로를 나타낸다.
type Workflow struct {
	Name        string
	DisplayName string
	Description string
	Steps       []WorkflowStep
}

// WorkflowStep은 워크플로의 개별 단계이다.
type WorkflowStep struct {
	Title       string
	Description string
	Command     string // 제안할 gk 명령어
}

// defaultWorkflows는 gk guide에서 기본 제공하는 워크플로 목록이다.
var defaultWorkflows = []Workflow{
	{
		Name:        "save",
		DisplayName: "변경사항 저장하기",
		Description: "파일 수정 → 커밋 → 서버에 올리기",
		Steps: []WorkflowStep{
			{
				Title:       "현재 상태 확인",
				Description: "어떤 파일이 수정되었는지 확인합니다.",
				Command:     "gk status",
			},
			{
				Title:       "변경사항 저장",
				Description: "수정한 내용을 커밋(변경사항 저장)합니다.",
				Command:     "gk commit",
			},
			{
				Title:       "서버에 올리기",
				Description: "저장한 커밋을 원격 저장소에 올립니다.",
				Command:     "gk push",
			},
		},
	},
	{
		Name:        "update",
		DisplayName: "서버에서 최신 코드 가져오기",
		Description: "원격 저장소의 최신 변경사항을 내 코드에 반영",
		Steps: []WorkflowStep{
			{
				Title:       "최신 코드 가져오기",
				Description: "원격 저장소에서 최신 변경사항을 가져와 내 코드에 반영합니다.",
				Command:     "gk pull",
			},
		},
	},
	{
		Name:        "branch-work",
		DisplayName: "브랜치 만들고 작업하기",
		Description: "새 작업 갈래를 만들어 독립적으로 작업",
		Steps: []WorkflowStep{
			{
				Title:       "새 브랜치 만들기",
				Description: "새로운 작업 갈래(브랜치)를 만들고 전환합니다.",
				Command:     "gk switch -c <이름>",
			},
			{
				Title:       "작업 후 저장",
				Description: "브랜치에서 작업한 내용을 커밋합니다.",
				Command:     "gk commit",
			},
			{
				Title:       "브랜치 합치기",
				Description: "작업한 브랜치를 메인 브랜치에 합칩니다.",
				Command:     "gk merge main",
			},
		},
	},
	{
		Name:        "resolve-conflict",
		DisplayName: "충돌 해결하기",
		Description: "같은 파일을 다르게 수정했을 때 해결하는 방법",
		Steps: []WorkflowStep{
			{
				Title:       "충돌 파일 편집",
				Description: "충돌이 발생한 파일을 열어 어떤 내용을 유지할지 선택합니다.",
				Command:     "gk edit-conflict",
			},
			{
				Title:       "해결 후 계속",
				Description: "충돌을 해결한 뒤 중단된 작업을 이어서 진행합니다.",
				Command:     "gk continue",
			},
		},
	},
	{
		Name:        "undo",
		DisplayName: "실수 되돌리기",
		Description: "잘못된 작업을 안전하게 되돌리기",
		Steps: []WorkflowStep{
			{
				Title:       "되돌리기",
				Description: "마지막 작업을 안전하게 되돌립니다.",
				Command:     "gk undo",
			},
			{
				Title:       "또는 타임머신",
				Description: "과거 상태 목록을 확인하고 원하는 시점으로 돌아갑니다.",
				Command:     "gk timemachine list",
			},
		},
	},
}
