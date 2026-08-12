param(
    [string]$UpstreamRef = "v0.1.173"
)

$ErrorActionPreference = "Stop"
$allowed = @(
    "README.md",
    ".gitignore",
    ".goreleaser.simple.yaml",
    ".goreleaser.yaml",
    ".github/workflows/upstream-compatibility.yml",
    "backend/cmd/server/wire.go",
    "backend/cmd/server/VERSION",
    "backend/cmd/server/wire_gen.go",
    "backend/cmd/server/wire_gen_test.go",
    "backend/internal/domain/constants.go",
    "backend/internal/handler/admin/group_handler.go",
    "backend/internal/handler/admin/group_handler_media_test.go",
    "backend/internal/media/",
    "backend/internal/server/http.go",
    "backend/internal/server/middleware/api_key_auth.go",
    "backend/internal/server/router.go",
    "backend/internal/server/routes/media.go",
    "backend/internal/service/admin_group.go",
    "backend/internal/service/domain_constants.go",
    "backend/internal/service/update_service.go",
    "backend/internal/service/update_service_test.go",
    "backend/migrations/9001_media_platform.sql",
    "deploy/.env.example",
    "deploy/docker-compose.local.yml",
    "deploy/docker-compose.yml",
    "docs/MEDIA_PLATFORM.md",
    "frontend/src/types/index.ts",
    "frontend/src/views/admin/GroupsView.vue",
    "tools/verify-media-fork.ps1"
)

$changed = git diff --name-only "$UpstreamRef...HEAD"
$unexpected = @($changed | Where-Object {
    $path = $_
    -not ($allowed | Where-Object {
        $path -eq $_ -or ($_.EndsWith("/") -and $path.StartsWith($_))
    })
})

if ($unexpected.Count -gt 0) {
    Write-Error "Unexpected files outside the Media fork boundary: $($unexpected -join ', ')"
}

Write-Host "Verified Media fork boundary against $UpstreamRef ($($changed.Count) changed files)."
