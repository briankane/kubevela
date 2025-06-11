// addon.cue

#EnableAddon: {
	#do:       "enable"
	#provider: "addon"

	$params: {
		name:      				string
		version: 	 			string
		overrideDefs?: 		bool | *false
		skipValidations?: bool | *false
		args?: 						[string]: _
	}

//	$returns: {
//		installed: 	bool
//		appName?: 	string
//		namespace?: string
//	}
}