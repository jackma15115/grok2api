import asyncio
import tempfile
from pathlib import Path
from types import SimpleNamespace

from app.control.account.enums import FeedbackKind
from app.control.account.backends.local import LocalAccountRepository
from app.control.account.commands import AccountPatch, AccountUpsert
from app.control.account.enums import QuotaSource
from app.control.account.models import AccountRecord, QuotaWindow
from app.control.account.quota_defaults import (
    default_quota_window,
    normalize_quota_window,
    supported_mode_ids,
    supports_mode,
)
from app.control.account.refresh import AccountRefreshService
from app.dataplane.account import AccountDirectory
from app.dataplane.account.selector import set_strategy
from app.dataplane.shared.enums import ModeId, PoolId
from app.platform.errors import UpstreamError
from app.platform.startup.migration import _backfill_console_quota


def test_console_supported_only_for_basic_pool():
    assert supports_mode("basic", 5)
    assert 5 in supported_mode_ids("basic")

    assert not supports_mode("super", 5)
    assert 5 not in supported_mode_ids("super")

    assert not supports_mode("heavy", 5)
    assert 5 not in supported_mode_ids("heavy")


def test_normalize_console_repairs_zero_without_reset():
    broken = QuotaWindow(
        remaining=0,
        total=0,
        window_seconds=0,
        reset_at=None,
        synced_at=None,
        source=QuotaSource.ESTIMATED,
    )

    repaired = normalize_quota_window("basic", 5, broken)

    assert repaired == default_quota_window("basic", 5)


def test_console_local_use_repairs_broken_quota_then_decrements():
    async def run():
        broken = QuotaWindow(
            remaining=0,
            total=0,
            window_seconds=0,
            reset_at=None,
            synced_at=None,
            source=QuotaSource.ESTIMATED,
        )
        record = AccountRecord(
            token="token-a",
            pool="basic",
            quota={"console": broken.to_dict()},
        )
        repo = _MemoryRepo(record)
        svc = AccountRefreshService(repo)

        await svc.refresh_call_async("token-a", 5)

        console = repo.record.quota_set().console
        assert console is not None
        assert console.remaining == 29
        assert console.total == 30
        assert console.window_seconds == 900
        assert repo.record.usage_use_count == 1

    asyncio.run(run())


def test_console_rate_limit_failure_does_not_zero_persisted_quota():
    async def run():
        window = default_quota_window("basic", 5)
        record = AccountRecord(
            token="token-a",
            pool="basic",
            quota={"console": window.to_dict()},
        )
        repo = _MemoryRepo(record)
        svc = AccountRefreshService(repo)

        await svc.record_failure_async("token-a", 5, UpstreamError("limited", status=429))

        console = repo.record.quota_set().console
        assert console == window
        assert repo.record.usage_fail_count == 1
        assert repo.record.last_fail_reason == "rate_limited"

    asyncio.run(run())


def test_bulk_quota_refresh_excludes_local_console_mode(monkeypatch):
    async def run():
        captured = {}

        async def fake_fetch_all_quotas(token, mode_ids):
            captured["token"] = token
            captured["mode_ids"] = mode_ids
            return None

        import app.dataplane.reverse.protocol.xai_usage as xai_usage

        monkeypatch.setattr(xai_usage, "fetch_all_quotas", fake_fetch_all_quotas)

        svc = AccountRefreshService(SimpleNamespace())
        result = await svc._fetch_all_quotas("token-a", "basic")

        assert result is None
        assert captured == {"token": "token-a", "mode_ids": (1,)}

    asyncio.run(run())


def test_console_backfill_repairs_persisted_zero_quota():
    async def run():
        with tempfile.TemporaryDirectory() as tmp:
            repo = LocalAccountRepository(Path(tmp) / "accounts.db")
            repo._init_sync()
            await repo.upsert_accounts([AccountUpsert(token="token-a", pool="basic")])
            broken = QuotaWindow(
                remaining=0,
                total=0,
                window_seconds=0,
                reset_at=None,
                synced_at=None,
                source=QuotaSource.ESTIMATED,
            )
            await repo.patch_accounts(
                [AccountPatch(token="token-a", quota_console=broken.to_dict())]
            )

            await _backfill_console_quota(repo)

            record = (await repo.get_accounts(["token-a"]))[0]
            assert record.quota_set().console == default_quota_window("basic", 5)

    asyncio.run(run())


def test_console_runtime_window_resets_after_local_exhaustion():
    async def run():
        set_strategy("quota")
        repo = _RuntimeRepo([
            AccountRecord(
                token="token-a",
                pool="basic",
                quota={"console": QuotaWindow(
                    remaining=1,
                    total=30,
                    window_seconds=900,
                    reset_at=None,
                    synced_at=None,
                    source=QuotaSource.DEFAULT,
                ).to_dict()},
            )
        ])
        directory = AccountDirectory(repo)
        await directory.bootstrap()

        lease = await directory.reserve(
            int(PoolId.BASIC), int(ModeId.CONSOLE), now_s_override=100
        )
        assert lease is not None
        await directory.release(lease)
        await directory.feedback(
            lease.token, FeedbackKind.SUCCESS, int(ModeId.CONSOLE), now_s_val=100
        )

        assert await directory.reserve(
            int(PoolId.BASIC), int(ModeId.CONSOLE), now_s_override=999
        ) is None

        lease = await directory.reserve(
            int(PoolId.BASIC), int(ModeId.CONSOLE), now_s_override=1000
        )
        assert lease is not None
        await directory.release(lease)

    asyncio.run(run())


def test_console_runtime_repairs_legacy_zero_without_reset():
    async def run():
        set_strategy("quota")
        repo = _RuntimeRepo([
            AccountRecord(
                token="token-a",
                pool="basic",
                quota={"console": QuotaWindow(
                    remaining=0,
                    total=30,
                    window_seconds=900,
                    reset_at=None,
                    synced_at=None,
                    source=QuotaSource.ESTIMATED,
                ).to_dict()},
            )
        ])
        directory = AccountDirectory(repo)
        await directory.bootstrap()

        lease = await directory.reserve(
            int(PoolId.BASIC), int(ModeId.CONSOLE), now_s_override=100
        )
        assert lease is not None
        await directory.release(lease)

    asyncio.run(run())


class _MemoryRepo:
    def __init__(self, record: AccountRecord) -> None:
        self.record = record

    async def get_accounts(self, tokens):
        if self.record.token in tokens:
            return [self.record]
        return []

    async def patch_accounts(self, patches):
        patch = patches[0]
        quota = dict(self.record.quota)
        if patch.quota_console is not None:
            quota["console"] = patch.quota_console
        self.record = self.record.model_copy(
            update={
                "quota": quota,
                "usage_use_count": self.record.usage_use_count
                + (patch.usage_use_delta or 0),
                "usage_fail_count": self.record.usage_fail_count
                + (patch.usage_fail_delta or 0),
                "last_fail_reason": patch.last_fail_reason or self.record.last_fail_reason,
            }
        )
        return None


class _RuntimeRepo:
    def __init__(self, records):
        self._records = records

    async def runtime_snapshot(self):
        return SimpleNamespace(items=self._records, revision=1)
