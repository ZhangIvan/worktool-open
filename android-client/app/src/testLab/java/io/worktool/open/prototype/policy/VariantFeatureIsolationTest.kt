package io.worktool.open.prototype.policy

import org.junit.Assert.*
import org.junit.Test

/** Feature ceiling only. No Android component or device execution evidence. */
class VariantFeatureIsolationTest {
    @Test fun exactSourceSetCannotElevateFixtureMode() {
        assertEquals(false,VariantFeatures.ownFixtureAccessibility)
        assertTrue(ConnectionMode.SYNTHETIC_1_0.allowed(VariantFeatures.ownFixtureAccessibility))
        assertEquals(false,ConnectionMode.OWN_FIXTURE_1_1.allowed(VariantFeatures.ownFixtureAccessibility))
        val hello=ConnectionProfile.SYNTHETIC.hello()
        assertFalse(hello.has("drivers"));assertEquals("1.0",hello.getJSONArray("supportedVersions").getString(0))
    }
}
