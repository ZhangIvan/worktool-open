package io.worktool.open.prototype.accessibility

import android.content.Context
import android.content.ContextWrapper
import androidx.test.core.app.ApplicationProvider
import androidx.test.ext.junit.runners.AndroidJUnit4
import io.worktool.open.prototype.core.FixtureTarget
import io.worktool.open.prototype.core.FixtureTicket
import org.junit.Assert.*
import org.junit.Test
import org.junit.runner.RunWith
import java.io.File
import java.util.UUID

/** NOT_RUN: requires separately authorized Android test execution; no Accessibility enablement. */
@RunWith(AndroidJUnit4::class)
class FixtureReceiptStoreTest {
    private fun context():Context {val base=ApplicationProvider.getApplicationContext<Context>();val dir=File(base.noBackupFilesDir,"receipt-test-"+UUID.randomUUID()).apply{mkdirs()};return object:ContextWrapper(base){override fun getNoBackupFilesDir()=dir}}
    private fun ticket(id:String="ticket_demo",text:String="Synthetic fixture")=FixtureTicket(id,"command_demo","attempt_demo","a".repeat(64),FixtureReceiptStore.hash(text),FixtureTarget("io.worktool.open.prototype","io.worktool.open.prototype.ui.FixtureActivity"),7,2,3,5000)
    @Test fun consumedCommandCannotReplayAcrossStoreInstances(){val c=context();val first=FixtureReceiptStore(c);assertTrue(first.consume(ticket()));first.close();val reopened=FixtureReceiptStore(c);reopened.markInterruptedUnknown();assertFalse(reopened.consume(ticket("ticket_new")));assertNull(reopened.receipt("ticket_demo"));reopened.close();c.noBackupFilesDir.deleteRecursively()}
    @Test fun effectAndReceiptAreCommittedOnceBeforeRender(){val c=context();val store=FixtureReceiptStore(c);val t=ticket();assertTrue(store.consume(t));store.phase(t.ticketId,"click_requested");val receipt=store.commit(t,"Synthetic fixture");assertEquals(receipt,store.receipt(t.ticketId));assertTrue(receipt.renderText.contains(receipt.receiptId));try{store.commit(t,"Synthetic fixture");fail("duplicate commit")}catch(_:IllegalArgumentException){};assertEquals(receipt,store.receipt(t.ticketId));store.close();c.noBackupFilesDir.deleteRecursively()}
}
