/*
 * Copyright 2026 WorkTool-Open 贡献者（Apache-2.0，见仓库根 LICENSE）。
 */
package cn.worktool.open.core.protocol

import cn.worktool.open.core.protocol.canonical.Jcs
import cn.worktool.open.core.protocol.crypto.Digests
import cn.worktool.open.core.protocol.crypto.FrameAuth
import cn.worktool.open.core.protocol.crypto.Hex
import kotlinx.serialization.json.JsonArray
import kotlinx.serialization.json.jsonObject
import kotlinx.serialization.json.jsonPrimitive
import org.junit.Assert.assertEquals
import org.junit.Assert.assertNotEquals
import org.junit.Assert.assertTrue
import org.junit.Test

/**
 * 金标准向量逐字节复算（protocol/fixtures/hmac-vector.json；docs/03 T2.2 DoD：
 * **关闭 AUD-03 C-4**——C-9 修订后 seq 纳入 HMAC 输入式的端侧实现证明）。
 *
 * 对每个正例向量四重断言：
 * 1. 从夹具帧本身重算 payloadDigest == 向量声明值（JCS/SHA-256 管线正确）；
 * 2. inputString 逐字节等于向量声明原文；
 * 3. HMAC-SHA256(deviceTokenTest, inputString) 逐字节等于向量声明 hmac；
 * 4. 夹具帧 meta.hmac == 向量 hmac（夹具与向量一致）。
 * 反例（tamper_seq）：篡改 seq 后重算 hmac != 声明 hmac，且等于向量给出的
 * hmacOverTamperedInput——篡改 seq 必败（C-9）。
 */
class HmacVectorTest {

    private val vectorFile = FixtureFiles.element("hmac-vector.json").jsonObject
    private val deviceToken = Hex.decode(vectorFile["deviceTokenTest"]!!.jsonPrimitive.content)

    private fun vectors(): List<kotlinx.serialization.json.JsonObject> =
        (vectorFile["vectors"] as JsonArray).map { it.jsonObject }

    @Test
    fun `金标准向量逐字节复算-全部正例`() {
        val vectors = vectors()
        assertTrue("向量数不足", vectors.size >= 5)
        for (v in vectors) {
            val fixtureName = v["fixture"]!!.jsonPrimitive.content
            val frame = FixtureFiles.element(fixtureName).jsonObject
            val encryptType = v["encryptType"]!!.jsonPrimitive.content.toInt()

            // ① payloadDigest 重算（encryptType=0 → JCS(list)；=2 → encryptedList 原样）
            val recomputedDigest = if (encryptType == 2) {
                FrameAuth.payloadDigestOfEncrypted(frame["encryptedList"]!!.jsonPrimitive.content)
            } else {
                FrameAuth.payloadDigestOfList(frame["list"] ?: null)
            }
            assertEquals("$fixtureName payloadDigest", v["payloadDigest"]!!.jsonPrimitive.content, recomputedDigest)

            // ② inputString 逐字节
            val input = FrameAuth.inputString(
                commandId = v["commandId"]!!.jsonPrimitive.content,
                eventId = v["eventId"]!!.jsonPrimitive.content,
                seq = v["seq"]!!.jsonPrimitive.content.toLong(),
                ts = v["ts"]!!.jsonPrimitive.content.toLong(),
                nonce = v["nonce"]!!.jsonPrimitive.content,
                payloadDigest = recomputedDigest,
            )
            assertEquals("$fixtureName inputString", v["inputString"]!!.jsonPrimitive.content, input)

            // ③ HMAC 逐字节
            val hmac = Digests.hmacSha256Hex(deviceToken, input)
            assertEquals("$fixtureName hmac", v["hmac"]!!.jsonPrimitive.content, hmac)

            // ④ 夹具 meta.hmac 与向量一致
            assertEquals(
                "$fixtureName fixture meta.hmac",
                v["hmac"]!!.jsonPrimitive.content,
                frame["meta"]!!.jsonObject["hmac"]!!.jsonPrimitive.content,
            )
        }
    }

    @Test
    fun `空载荷摘要常量`() {
        assertEquals(
            "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855",
            Digests.SHA256_EMPTY_HEX,
        )
        assertEquals(Digests.SHA256_EMPTY_HEX, FrameAuth.payloadDigestOfList(null))
        assertEquals(Digests.SHA256_EMPTY_HEX, FrameAuth.payloadDigestOfList(JsonArray(emptyList())))
    }

    @Test
    fun `反例 tamper_seq 篡改必败`() {
        val neg = ((vectorFile["negative"] as JsonArray)[0]).jsonObject
        assertEquals("tamper_seq", neg["case"]!!.jsonPrimitive.content)

        val declared = neg["declaredHmac"]!!.jsonPrimitive.content
        val tamperedInput = neg["tamperedInputString"]!!.jsonPrimitive.content
        val hmacOverTampered = neg["hmacOverTamperedInput"]!!.jsonPrimitive.content

        // 断言 1：篡改 seq 后重算 HMAC != 声明 HMAC（C-9：seq 已纳入输入式）
        val recomputed = Digests.hmacSha256Hex(deviceToken, tamperedInput)
        assertNotEquals(declared, recomputed)

        // 断言 2：重算值 == 向量给出的 hmacOverTamperedInput（逐字节）
        assertEquals(hmacOverTampered, recomputed)

        // 输入式自证：篡改帧字段重排 inputString 与 tamperedInputString 一致
        val tamperedSeq = neg["tamperedSeq"]!!.jsonPrimitive.content.toLong()
        val base = vectors().first { it["fixture"]!!.jsonPrimitive.content == "command-envelope.plain.json" }
        val rebuilt = FrameAuth.inputString(
            commandId = base["commandId"]!!.jsonPrimitive.content,
            eventId = base["eventId"]!!.jsonPrimitive.content,
            seq = tamperedSeq,
            ts = base["ts"]!!.jsonPrimitive.content.toLong(),
            nonce = base["nonce"]!!.jsonPrimitive.content,
            payloadDigest = base["payloadDigest"]!!.jsonPrimitive.content,
        )
        assertEquals(tamperedInput, rebuilt)
    }

    @Test
    fun `JCS 对 exec-result 嵌套载荷可复算`() {
        // 额外自证：exec-result 向量的 digest 由 Jcs.canonicalize 直接可得
        val frame = FixtureFiles.element("command-envelope.exec-result.json").jsonObject
        val canonical = Jcs.canonicalize(frame["list"]!!)
        assertEquals(
            "e2b43c449ca4a52b58b80b254c4943a09fea8ae9b0fd86fe8fe5b2b254a06980",
            Digests.sha256Hex(canonical),
        )
    }
}
