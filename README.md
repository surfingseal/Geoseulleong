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
