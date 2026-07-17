package com.arnosvpn.android

import com.journeyapps.barcodescanner.CaptureActivity

/**
 * PortraitCaptureActivity is the ZXing scanner screen locked to portrait. The
 * library's default CaptureActivity opens sideways (landscape); pointing
 * ScanOptions at this subclass — declared with android:screenOrientation="portrait"
 * in the manifest — makes the scanner stand upright.
 */
class PortraitCaptureActivity : CaptureActivity()
