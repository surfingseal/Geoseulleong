# Geoseulleong Go API

광역버스 혼잡도·실시간 좌석 참고 정보와 대체 승차 경로를 제공하는 Go 백엔드입니다.

## 실행

Go 1.27, PostgreSQL 18 + PostGIS가 필요합니다.

```sh
export DATABASE_URL='postgres://USER:PASSWORD@HOST/geoseulleong?sslmode=require'
go run ./cmd/server
```

기본 로컬 주소는 `127.0.0.1:8090`이며 `PORT`가 설정되면 `0.0.0.0:$PORT`를 사용합니다.

- `GET /health`: PostgreSQL 연결 확인
- `GET /healthz`: 프로세스 확인
- `GET /api/routes/M5121`, `/stops`, `/shape`: 노선·정류장·GeoJSON
- `GET /api/v1/routes/{routeId}/congestion`: 혼잡도 통계
- `GET /api/v1/routes/{routeId}/congestion/visits/{visitId}`: 정류장·시간코드 통계
- `GET /api/v1/journeys`: 경로 조회
- `GET /api/v1/journeys/boarding-alternatives`: 대체 승차 후보
- `GET /api/v1/journeys/boarding-recommendations`: 대체 경로 비교
- `POST /api/v1/auth/signup`, `/login`, `/refresh`, `/logout`
- `GET /api/v1/users/me`
- `GET /swagger/`: OpenAPI와 Swagger UI

상세 요청 규칙은 Swagger의 명세를 사용합니다. 기존 추천 기본값은 `travel_time_only_v1`입니다.

## Heroku

앱 `geoseulleong`, 빌드팩 `heroku/go`, Procfile `web: bin/server`.
`GOVERSION=go1.27`, `GO_INSTALL_PACKAGE_SPEC=./cmd/server`를 설정하고 연결한 GitHub `main`을 **수동 배포**합니다. 자동 배포는 사용하지 않습니다.

`DATABASE_URL`은 기존 Heroku Postgres 연결값을 사용합니다. Dyno에서는 `sslmode=require` 이상이 강제됩니다. 기본 조회 풀은 read-only입니다. 인증과 예산 전용 쓰기 풀은 Heroku의 같은 `DATABASE_URL`을 재사용하며 자격증명 회전을 따릅니다. 로컬에서는 `AUTH_DATABASE_URL` 및 `BUDGET_DATABASE_URL`이 필요합니다.

`AUTH_ENABLED=true`일 때 `AUTH_JWT_SECRET_BASE64`(32바이트 이상 난수), `AUTH_TERMS_VERSION`, `AUTH_PRIVACY_VERSION`을 Config Vars에 설정합니다. 실제 서비스의 약관과 개인정보 버전은 별도로 관리해야 합니다. 비밀값은 저장소·응답에 포함하지 않습니다.

외부 교통 API는 `JOURNEY_SEARCH_MODE=disabled`, `BOARDING_REALTIME_ENABLED=false`, `ALTERNATIVE_GBIS_ENABLED=false`가 기본입니다. 키 등록만으로 호출되지 않습니다. ODsay/Kakao/GBIS 키는 서버 환경변수 `ODSAY_API_KEY`, `KAKAO_REST_API_KEY`, `GBIS_API_KEY`로만 읽습니다.

Live 활성화에는 별도 승인·IP 접근 확인·provider별 명시적 일일/누적 예산과 `API_BUDGET_BACKEND=postgres`가 필요합니다. `external_api_budgets`는 기존 ledger 사용량을 먼저 가져와야 합니다. 누락 scope를 0으로 자동 생성하지 않습니다. 전송 전 예약을 커밋하며 재시작·오류 시 환불하지 않습니다. 캐시·동시 요청 중복 방지·최소 2초 간격을 유지합니다. Heroku 파일 시스템을 호출 예산 저장소로 사용하지 않습니다.

기존 DB 백업을 복원한 경우 001~005를 다시 적용하지 않습니다. 백업 후 신규 테이블에만 `006_auth.sql`, `007_external_api_budgets.sql`을 적용합니다. 기존 데이터·매핑·원본 ID는 보존합니다.

## 개발

```sh
go test ./...
go build ./...
```

이 저장소는 배포용 서버 패키지를 포함합니다. 개발 PC의 연구 CLI·실측 원본·실험 fixture·DB 백업·상태 파일은 업로드하지 않습니다. 전체 기존 테스트는 원본 개발 프로젝트에서 수행하며, 이 저장소에는 원본 파일 없이 실행 가능한 배포 회귀 테스트를 포함합니다. `.env`, `results/`, `data/`, `var/`, `backups/`는 Git과 slug에서 제외합니다.


## Heroku 경로 검색 연결 (2026-11-21)

`/api/v1/journeys` 응답의 `search_id`, 선택한 `path_index`, 광역버스 `leg_index`를 `/api/v1/journeys/boarding-recommendations`에 전달합니다. 서버가 보관한 원본 인덱스를 사용하며 Android에서 새 인덱스를 붙이지 않습니다. 선택 정보는 신규 `008_journey_selections.sql` 테이블에 저장되어 10분 유효기간 내 dyno 재시작·다른 인스턴스에서도 재사용합니다. 기존 예산용 writable 풀을 공유하며 교통 데이터는 읽기 전용입니다. 만료는 410, 선택 저장소 장애는 503으로 구분합니다. 만료된 행은 조회에서 제외되며 물리적 보관·정리 정책은 별도 운영 항목입니다.

현재 Live 활성화 승인과 추가 누적 예산이 없으므로 `JOURNEY_SEARCH_MODE=disabled`, ODsay/Kakao/GBIS 승인 플래그 false를 유지합니다. 공식 ID 검증이 가능한 ODsay를 활성화하려면 별도 승인 후 `JOURNEY_PROVIDER=odsay`, `JOURNEY_SEARCH_MODE=live`, `ODSAY_LIVE_APPROVED=true`, 남은 일일·누적 예산, 접근 IP를 설정합니다. `API_BUDGET_BACKEND=postgres`와 기존 ledger를 유지해야 하며 카운터 초기화는 금지합니다. 이때 ODsay 접근/광역버스 구간 조회는 같은 클라이언트·예산·캐시를 공유합니다. Kakao도 기존 provider 설정을 지원하지만 공식 정류장 ID가 없으면 대체 승차 확정 매핑을 거부합니다. GBIS는 별도의 승인·예산으로만 활성화합니다.

Swagger `/swagger/`의 경로 검색과 대체 경로 요청·응답 예시를 사용하세요. `reference_date`/`service_date`는 과거 통계 참고일이며 현재 GBIS 값과 구분합니다. 전체 시간은 확보한 구간만 합산하고 누락 시 null, ETA·좌석 미제공도 unknown/null로 남깁니다.


## 카카오 로그인

`POST /api/v1/auth/kakao`에 Android SDK의 `access_token`을 전달합니다. 최초 가입에는 `terms_agreed=true`, `privacy_agreed=true`가 필요하고, 재로그인은 토큰만 전달합니다. 토큰 정보의 app_id·만료 및 사용자 정보의 id를 검증한 뒤 기존 Geoseulleong Session을 발급합니다. 카카오 토큰은 저장하거나 로그에 출력하지 않습니다.

서버 환경변수: `KAKAO_LOGIN_APP_ID=1652139` (숫자 앱 ID). 이 방식의 서버 검증에는 REST/native/admin 키가 필요하지 않습니다. 기존 `KAKAO_REST_API_KEY`는 경로 제공자용이며 로그인 검증에 사용하지 않습니다. 앱 ID 미설정 시 새 API만 503이고 기존 로그인은 유지합니다.

DB 백업 후 `migrations/009_kakao_login.sql`을 적용합니다. users의 email/password_hash를 nullable로 변경하고 `oauth_identities(provider,provider_user_id,user_id)`를 추가합니다. 기존 이메일 UNIQUE 및 일반 가입 비밀번호 검증은 유지합니다. OAuth 계정은 users.email=NULL, password_hash=NULL이며 응답 email은 기존 string 계약에 맞춰 빈 문자열입니다. 이메일 자동 연결과 로컬 비밀번호 자동 생성은 하지 않습니다. 사용자·provider 연결·동의·Refresh Token 생성은 하나의 트랜잭션으로 처리합니다.

카카오 디벨로퍼스에서 로그인 사용을 활성화하고 Android의 실제 applicationId(첨부 화면: com.example.salmon)와 디버그/릴리스 키 해시를 같은 앱에 등록해야 합니다. Android SDK는 해당 앱의 네이티브 앱 키를 사용합니다. 스토어 URL은 배포 상태에 맞게 설정하며 백엔드는 인가코드를 교환하지 않으므로 OAuth callback 엔드포인트를 추가하지 않습니다. 이메일은 필수 동의항목이 아닙니다. SDK 설정과 테스트 계정 준비 후 실제 토큰으로 통합 확인해야 합니다.

공식 명세: https://developers.kakao.com/docs/ko/kakaologin/rest-api (access_token_info, v2/user/me). 현재 구현 테스트는 mock이며 실제 카카오 계정 로그인은 수행하지 않습니다. Swagger `/swagger/`에 최초/재로그인 요청과 동일 Session 응답을 제공합니다.
